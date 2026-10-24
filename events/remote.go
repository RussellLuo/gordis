package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/RussellLuo/gordis"
)

// RemoteHandlerKind is the wire-neutral kind of one remote subscription.
// It is exported for optional execution adapters; ordinary plugins use On,
// Handle, and Use instead.
type RemoteHandlerKind uint8

const (
	RemoteNotification RemoteHandlerKind = iota + 1
	RemoteSelection
	RemoteMiddleware
)

// RemoteDispatchMode is the wire-neutral form of the local dispatch methods.
type RemoteDispatchMode uint8

const (
	RemoteEmit RemoteDispatchMode = iota + 1
	RemoteParallel
	RemoteSerial
	RemoteWaterfall
)

// RemoteOptions is the serializable subset of OnOption.
type RemoteOptions struct {
	Once     bool
	Global   bool
	Priority int
}

// OwnerRef identifies the actual caller Scope within one transport session.
type OwnerRef struct {
	Instance   string
	Generation uint64
}

// RemoteSubscription describes a subscription before an execution adapter
// assigns its generation-scoped host ID.
type RemoteSubscription struct {
	Owner    OwnerRef
	LocalID  uint64
	Contract Contract
	Kind     RemoteHandlerKind
	Options  RemoteOptions
}

// RemoteDispatch carries typed local values to an execution adapter. The
// adapter is responsible for applying an explicitly registered codec.
type RemoteDispatch struct {
	Owner    OwnerRef
	Contract Contract
	Mode     RemoteDispatchMode
	Payload  any
	Source   *gordis.ServiceSpec
	Base     any
}

// RemoteResult is the common result of selection and waterfall dispatch.
type RemoteResult struct {
	Value   any
	Handled bool
}

// RemoteTransport is implemented by optional execution adapters. Values are
// still typed here; serialization is deliberately outside the local EventBus.
type RemoteTransport interface {
	Subscribe(context.Context, RemoteSubscription) (hostID uint64, err error)
	Unsubscribe(context.Context, uint64) error
	Dispatch(context.Context, RemoteDispatch) (RemoteResult, error)
}

// RemoteSubscriptionCommitter enables a subscription only after its local
// handler and cleanup are installed. New transports should implement it.
type RemoteSubscriptionCommitter interface {
	CommitSubscription(context.Context, uint64) error
}

// RemoteOwnerTransport coordinates the actual subscribing/publishing Scope.
// RetireOwner must be idempotent and reject later registration/Ready/Commit.
// Older transports may omit this interface during migration.
type RemoteOwnerTransport interface {
	RegisterOwner(context.Context, OwnerRef) error
	OwnerReady(context.Context, OwnerRef) error
	RetireOwner(context.Context, OwnerRef) error
}

// RemoteMiddlewareObserver closes remote continuation admission at the actual
// middleware return, before Deliver waits for already admitted next work.
// Implementations must use bounded control work and must not wait for next.
type RemoteMiddlewareObserver interface {
	MiddlewareReturned(context.Context) error
}

// RemoteEndpoint is a Bus backed by an execution adapter. Deliver is called by
// that adapter for a Host-admitted remote subscription.
type RemoteEndpoint interface {
	Bus
	// RevokeSubscription disables a Host-consumed subscription locally. It
	// does not wait for admitted handlers or make a transport call. Final
	// Scope cleanup still confirms the idempotent remote removal.
	RevokeSubscription(uint64)
	Deliver(
		context.Context,
		uint64,
		any,
		func(context.Context, any) (any, error),
	) (RemoteResult, error)
}

func (b *remoteBus) RevokeSubscription(hostID uint64) {
	b.mu.Lock()
	delete(b.byHostID, hostID)
	delete(b.removals, hostID)
	b.mu.Unlock()
}

type remoteBus struct {
	mu        sync.Mutex
	transport RemoteTransport
	nextID    uint64
	byHostID  map[uint64]*subscription
	owners    map[*gordis.Scope]*remoteOwner
	removals  map[uint64]*remoteRemoval
}

// NewRemoteBus creates the process-side mirror of a Host EventBus. It does not
// provide local routing: every subscription and dispatch crosses transport and
// must therefore have an adapter-provided wire binding.
func NewRemoteBus(transport RemoteTransport) RemoteEndpoint {
	return &remoteBus{transport: transport, byHostID: map[uint64]*subscription{}, owners: map[*gordis.Scope]*remoteOwner{}, removals: map[uint64]*remoteRemoval{}}
}

func remoteKind(kind handlerKind) RemoteHandlerKind {
	switch kind {
	case handlerNotification:
		return RemoteNotification
	case handlerSelection:
		return RemoteSelection
	case handlerMiddleware:
		return RemoteMiddleware
	default:
		return 0
	}
}

func remoteMode(kind dispatchKind) RemoteDispatchMode {
	switch kind {
	case dispatchEmit:
		return RemoteEmit
	case dispatchParallel:
		return RemoteParallel
	case dispatchSerial:
		return RemoteSerial
	case dispatchWaterfall:
		return RemoteWaterfall
	default:
		return 0
	}
}

func (b *remoteBus) subscribe(
	scope *gordis.Scope,
	contract Contract,
	handler any,
	opts subscriptionOptions,
) (func(), error) {
	if b == nil || b.transport == nil || scope == nil || contract == nil {
		return nil, gordis.ErrClosed
	}
	spec := contract.eventContract()
	if err := validateContract(spec); err != nil {
		return nil, err
	}
	kind, value, err := inspectHandler(spec, handler)
	if err != nil {
		return nil, err
	}
	release, err := scope.Acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	owner, err := b.owner(scope)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.nextID++
	localID := b.nextID
	b.mu.Unlock()
	subscribeCtx, cancelSubscribe := context.WithTimeout(scope.Context(), remoteControlTimeout)
	hostID, err := b.transport.Subscribe(subscribeCtx, RemoteSubscription{
		Owner: owner.ref, LocalID: localID, Contract: contract, Kind: remoteKind(kind),
		Options: RemoteOptions{Once: opts.once, Global: opts.global, Priority: opts.priority},
	})
	cancelSubscribe()
	if err != nil {
		return nil, err
	}
	if hostID == 0 {
		return nil, errors.New("events: remote transport returned a zero subscription ID")
	}
	sub := &subscription{
		id: localID, contract: spec, kind: kind, handler: value, scope: scope,
		once: opts.once, global: opts.global, priority: opts.priority, active: true,
	}
	removal := &remoteRemoval{bus: b, id: hostID, sub: sub}
	b.mu.Lock()
	_, duplicate := b.byHostID[hostID]
	if duplicate || owner.retiring {
		b.mu.Unlock()
		// A duplicate ID must never overwrite an existing local handler.
		cleanupErr := removal.remove(context.Background())
		if duplicate {
			return nil, errors.Join(errors.New("events: duplicate remote subscription ID"), cleanupErr)
		}
		return nil, errors.Join(gordis.ErrClosed, cleanupErr)
	}
	b.byHostID[hostID] = sub
	b.removals[hostID] = removal
	b.mu.Unlock()
	if err := scope.Defer(fmt.Sprintf("remote events subscription %d", hostID), removal.remove); err != nil {
		return nil, errors.Join(err, removal.remove(context.Background()))
	}
	if committer, ok := b.transport.(RemoteSubscriptionCommitter); ok {
		ctx, cancel := context.WithTimeout(scope.Context(), remoteControlTimeout)
		err = committer.CommitSubscription(ctx, hostID)
		cancel()
		if err != nil {
			return nil, errors.Join(err, removal.remove(context.Background()))
		}
	}
	b.mu.Lock()
	retired := owner.retiring
	b.mu.Unlock()
	if retired {
		return nil, errors.Join(gordis.ErrClosed, removal.remove(context.Background()))
	}
	return func() { _ = removal.remove(context.Background()) }, nil
}

func (b *remoteBus) dispatch(
	ctx context.Context,
	publisher *gordis.Scope,
	contract contractSpec,
	payload any,
	kind dispatchKind,
	source *sourceAddress,
	base any,
) (any, bool, error) {
	if b == nil || b.transport == nil || publisher == nil {
		return nil, false, gordis.ErrClosed
	}
	if ctx == nil {
		return nil, false, errors.New("events: nil context")
	}
	if err := validateContract(contract); err != nil {
		return nil, false, err
	}
	owner, err := b.owner(publisher)
	if err != nil {
		return nil, false, err
	}
	release, err := publisher.AcquireReady()
	if err != nil {
		return nil, false, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	var sourceSpec *gordis.ServiceSpec
	if source != nil {
		if !publisher.HasProvided(source.service) {
			return nil, false, fmt.Errorf("events: current generation did not publish service %q", source.name)
		}
		copy := source.service
		sourceSpec = &copy
	}
	result, err := b.transport.Dispatch(ctx, RemoteDispatch{
		Owner: owner.ref, Contract: remoteContract{contract}, Mode: remoteMode(kind), Payload: payload,
		Source: sourceSpec, Base: base,
	})
	return result.Value, result.Handled, err
}

type remoteContract struct{ spec contractSpec }

func (c remoteContract) Name() string                { return c.spec.name }
func (c remoteContract) ID() string                  { return c.spec.id }
func (c remoteContract) eventContract() contractSpec { return c.spec }

// Deliver validates the host subscription ID against this remote generation,
// then applies the same Ready admission and handler semantics as local delivery.
func (b *remoteBus) Deliver(
	ctx context.Context,
	hostID uint64,
	payload any,
	next func(context.Context, any) (any, error),
) (result RemoteResult, err error) {
	if ctx == nil {
		return RemoteResult{}, errors.New("events: nil context")
	}
	b.mu.Lock()
	sub := b.byHostID[hostID]
	b.mu.Unlock()
	if sub == nil {
		return RemoteResult{}, gordis.ErrStale
	}
	release, err := sub.scope.AcquireReady()
	if err != nil {
		return RemoteResult{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return RemoteResult{}, err
	}
	if (payload != nil && reflect.TypeOf(payload) != sub.contract.input) || (payload == nil && !nullableType(sub.contract.input)) {
		return RemoteResult{}, errors.New("events: remote payload has incompatible type")
	}
	if sub.kind == handlerMiddleware && next == nil {
		return RemoteResult{}, errors.New("events: remote middleware has no continuation")
	}
	b.mu.Lock()
	if b.byHostID[hostID] != sub {
		b.mu.Unlock()
		return RemoteResult{}, gordis.ErrStale
	}
	removal := b.removals[hostID]
	if sub.once {
		delete(b.byHostID, hostID)
	}
	b.mu.Unlock()
	if sub.once && removal != nil {
		defer func() { err = errors.Join(err, removal.remove(context.Background())) }()
	}
	switch sub.kind {
	case handlerNotification:
		return RemoteResult{}, callNotification(sub, ctx, payload)
	case handlerSelection:
		value, handled, err := callSelection(sub, ctx, payload)
		return RemoteResult{Value: value, Handled: handled}, err
	case handlerMiddleware:
		if next == nil {
			return RemoteResult{}, errors.New("events: remote middleware has no continuation")
		}
		var returned func() error
		if observer, ok := b.transport.(RemoteMiddlewareObserver); ok {
			returned = func() error { return observer.MiddlewareReturned(ctx) }
		}
		value, err := callMiddlewareReturned(sub, ctx, payload, next, returned)
		return RemoteResult{Value: value, Handled: err == nil}, err
	default:
		return RemoteResult{}, errors.New("events: invalid remote handler kind")
	}
}

// DispatchRemote lets an execution adapter enter the local Bus after it has
// decoded a registered wire contract. Ordinary plugins use the typed methods.
func (e Binding) DispatchRemote(ctx context.Context, request RemoteDispatch) (RemoteResult, error) {
	if request.Contract == nil {
		return RemoteResult{}, errors.New("events: nil remote contract")
	}
	kind := dispatchKind(0)
	switch request.Mode {
	case RemoteEmit:
		kind = dispatchEmit
	case RemoteParallel:
		kind = dispatchParallel
	case RemoteSerial:
		kind = dispatchSerial
	case RemoteWaterfall:
		kind = dispatchWaterfall
	default:
		return RemoteResult{}, errors.New("events: invalid remote dispatch mode")
	}
	var source *sourceAddress
	if request.Source != nil {
		var err error
		source, err = e.source(*request.Source, *request.Source)
		if err != nil {
			return RemoteResult{}, err
		}
	}
	value, handled, err := e.dispatch(
		ctx, request.Contract.eventContract(), request.Payload, kind, source, request.Base,
	)
	return RemoteResult{Value: value, Handled: handled}, err
}

const remoteControlTimeout = 4 * time.Second

func nullableType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
		return true
	}
	return false
}

type remoteRemoval struct {
	mu        sync.Mutex
	bus       *remoteBus
	id        uint64
	sub       *subscription
	confirmed bool
}

func (r *remoteRemoval) remove(ctx context.Context) error {
	b := r.bus
	b.mu.Lock()
	if b.byHostID[r.id] == r.sub {
		delete(b.byHostID, r.id)
	}
	b.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.confirmed {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, remoteControlTimeout)
	defer cancel()
	if err := b.transport.Unsubscribe(ctx, r.id); err != nil {
		return err
	}
	r.confirmed = true
	b.mu.Lock()
	if b.removals[r.id] == r {
		delete(b.removals, r.id)
	}
	b.mu.Unlock()
	return nil
}

type remoteOwner struct {
	ref      OwnerRef
	done     chan struct{}
	err      error // published by closing done
	retiring bool  // protected by remoteBus.mu
}

func (b *remoteBus) owner(scope *gordis.Scope) (*remoteOwner, error) {
	release, err := scope.Acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	b.mu.Lock()
	owner := b.owners[scope]
	if owner != nil {
		retiring := owner.retiring
		b.mu.Unlock()
		if retiring {
			return nil, gordis.ErrClosed
		}
		select {
		case <-owner.done:
			return owner, owner.err
		case <-scope.Context().Done():
			return nil, scope.Context().Err()
		}
	}
	owner = &remoteOwner{ref: OwnerRef{scope.ID(), scope.Generation()}, done: make(chan struct{})}
	b.owners[scope] = owner
	b.mu.Unlock()
	transport, coordinated := b.transport.(RemoteOwnerTransport)
	retire := func(ctx context.Context) error {
		b.mu.Lock()
		owner.retiring = true
		for id, sub := range b.byHostID {
			if sub.scope == scope {
				delete(b.byHostID, id)
			}
		}
		b.mu.Unlock()
		if !coordinated {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, remoteControlTimeout)
		defer cancel()
		return transport.RetireOwner(ctx, owner.ref)
	}
	// Install retirement before registration. A stop racing registration may
	// establish the remote tombstone first; registration must not revive it.
	err = scope.Defer("remote events owner", retire)
	if err == nil {
		err = scope.OnStop("remote events owner", retire)
	}
	if err == nil && coordinated {
		err = scope.AfterReady("remote events owner ready", func(ctx context.Context) error {
			select {
			case <-owner.done:
			case <-ctx.Done():
				return ctx.Err()
			}
			b.mu.Lock()
			retired := owner.retiring
			b.mu.Unlock()
			if retired {
				return nil
			}
			if owner.err != nil {
				return owner.err
			}
			ctx, cancel := context.WithTimeout(ctx, remoteControlTimeout)
			defer cancel()
			return transport.OwnerReady(ctx, owner.ref)
		})
	}
	if err == nil && coordinated {
		ctx, cancel := context.WithTimeout(scope.Context(), remoteControlTimeout)
		err = transport.RegisterOwner(ctx, owner.ref)
		cancel()
	}
	if err != nil {
		err = errors.Join(err, retire(context.Background()))
	}
	b.mu.Lock()
	retired := owner.retiring
	b.mu.Unlock()
	if retired && err == nil {
		err = gordis.ErrClosed
	}
	owner.err = err
	close(owner.done)
	return owner, err
}
