package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

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

// RemoteSubscription describes a subscription before an execution adapter
// assigns its generation-scoped host ID.
type RemoteSubscription struct {
	LocalID  uint64
	Contract Contract
	Kind     RemoteHandlerKind
	Options  RemoteOptions
}

// RemoteDispatch carries typed local values to an execution adapter. The
// adapter is responsible for applying an explicitly registered codec.
type RemoteDispatch struct {
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

// RemoteEndpoint is a Bus backed by an execution adapter. Deliver is called by
// that adapter for a Host-admitted remote subscription.
type RemoteEndpoint interface {
	Bus
	Deliver(
		context.Context,
		uint64,
		any,
		func(context.Context, any) (any, error),
	) (RemoteResult, error)
}

type remoteBus struct {
	mu        sync.Mutex
	transport RemoteTransport
	nextID    uint64
	byHostID  map[uint64]*subscription
}

// NewRemoteBus creates the process-side mirror of a Host EventBus. It does not
// provide local routing: every subscription and dispatch crosses transport and
// must therefore have an adapter-provided wire binding.
func NewRemoteBus(transport RemoteTransport) RemoteEndpoint {
	return &remoteBus{transport: transport, byHostID: map[uint64]*subscription{}}
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
	b.mu.Lock()
	b.nextID++
	localID := b.nextID
	b.mu.Unlock()
	hostID, err := b.transport.Subscribe(scope.Context(), RemoteSubscription{
		LocalID: localID, Contract: contract, Kind: remoteKind(kind),
		Options: RemoteOptions{Once: opts.once, Global: opts.global, Priority: opts.priority},
	})
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
	b.mu.Lock()
	if _, exists := b.byHostID[hostID]; exists {
		b.mu.Unlock()
		_ = b.transport.Unsubscribe(context.Background(), hostID)
		return nil, errors.New("events: duplicate remote subscription ID")
	}
	b.byHostID[hostID] = sub
	b.mu.Unlock()

	var once sync.Once
	remove := func(ctx context.Context) error {
		var removeErr error
		once.Do(func() {
			b.mu.Lock()
			if b.byHostID[hostID] == sub {
				delete(b.byHostID, hostID)
			}
			b.mu.Unlock()
			removeErr = b.transport.Unsubscribe(ctx, hostID)
		})
		return removeErr
	}
	if err := scope.Defer(fmt.Sprintf("remote events subscription %d", hostID), func(ctx context.Context) error {
		return remove(ctx)
	}); err != nil {
		_ = remove(context.Background())
		return nil, err
	}
	return func() { _ = remove(context.Background()) }, nil
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
		if !publisher.Provides(source.service) {
			return nil, false, fmt.Errorf("events: current generation did not publish service %q", source.name)
		}
		copy := source.service
		sourceSpec = &copy
	}
	result, err := b.transport.Dispatch(ctx, RemoteDispatch{
		Contract: remoteContract{contract}, Mode: remoteMode(kind), Payload: payload,
		Source: sourceSpec, Base: base,
	})
	return result.Value, result.Handled, err
}

type remoteContract struct{ spec contractSpec }

func (c remoteContract) Name() string                { return c.spec.name }
func (c remoteContract) ID() string                  { return c.spec.id }
func (c remoteContract) eventContract() contractSpec { return c.spec }

func (b *remoteBus) claim(hostID uint64) (*subscription, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := b.byHostID[hostID]
	if sub == nil || !sub.active {
		return nil, false
	}
	if sub.once {
		delete(b.byHostID, hostID)
	}
	return sub, true
}

// Deliver validates the host subscription ID against this remote generation,
// then applies the same Ready admission and handler semantics as local delivery.
func (b *remoteBus) Deliver(
	ctx context.Context,
	hostID uint64,
	payload any,
	next func(context.Context, any) (any, error),
) (RemoteResult, error) {
	if ctx == nil {
		return RemoteResult{}, errors.New("events: nil context")
	}
	sub, ok := b.claim(hostID)
	if !ok {
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
	if payload != nil && reflect.TypeOf(payload) != sub.contract.input {
		return RemoteResult{}, errors.New("events: remote payload has incompatible type")
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
		value, err := callMiddleware(sub, ctx, payload, next)
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
