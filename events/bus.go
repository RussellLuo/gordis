package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/RussellLuo/gordis"
)

var (
	ErrNoHandler   = errors.New("events: no handler")
	ErrNextCalled  = errors.New("events: next called more than once")
	ErrNextExpired = errors.New("events: next is no longer active")
)

type handlerKind uint8

const (
	handlerNotification handlerKind = iota + 1
	handlerSelection
	handlerMiddleware
)

type dispatchKind uint8

const (
	dispatchEmit dispatchKind = iota + 1
	dispatchParallel
	dispatchSerial
	dispatchWaterfall
)

type sourceAddress struct {
	name    string
	label   string
	service gordis.ServiceSpec
}

type subscription struct {
	id       uint64
	contract contractSpec
	kind     handlerKind
	handler  reflect.Value
	scope    *gordis.Scope
	view     gordis.View
	label    string
	priority int
	sequence uint64
	once     bool
	global   bool
	active   bool
}

type localBus struct {
	mu            sync.Mutex
	closed        bool
	nextID        uint64
	nextSequence  uint64
	contracts     map[string]contractSpec
	subscriptions map[uint64]*subscription
	parallel      chan struct{}
}

func newLocalBus(parallelism int) *localBus {
	return &localBus{
		contracts: map[string]contractSpec{}, subscriptions: map[uint64]*subscription{},
		parallel: make(chan struct{}, parallelism),
	}
}

func (b *localBus) close() {
	b.mu.Lock()
	b.closed = true
	b.subscriptions = map[uint64]*subscription{}
	b.mu.Unlock()
}

func validateContract(spec contractSpec) error {
	if spec.id == "" || spec.name != targetPrefix+spec.id || spec.input == nil {
		return errors.New("events: invalid contract")
	}
	if spec.kind == contractHook && spec.output == nil {
		return errors.New("events: invalid hook")
	}
	if spec.kind != contractTopic && spec.kind != contractHook {
		return errors.New("events: invalid contract kind")
	}
	return nil
}

func sameContract(a, b contractSpec) bool {
	return a.id == b.id && a.name == b.name && a.kind == b.kind && a.input == b.input && a.output == b.output
}

func (b *localBus) ensureContractLocked(spec contractSpec) error {
	if err := validateContract(spec); err != nil {
		return err
	}
	if old, ok := b.contracts[spec.id]; ok {
		if !sameContract(old, spec) {
			return fmt.Errorf("events: contract %q has conflicting type or kind", spec.id)
		}
		return nil
	}
	b.contracts[spec.id] = spec
	return nil
}

func (b *localBus) subscribe(scope *gordis.Scope, contract Contract, handler any, opts subscriptionOptions) (func(), error) {
	if scope == nil || contract == nil {
		return nil, gordis.ErrClosed
	}
	spec := contract.eventContract()
	kind, value, err := inspectHandler(spec, handler)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, gordis.ErrClosed
	}
	if err := b.ensureContractLocked(spec); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	b.nextID++
	b.nextSequence++
	id := b.nextID
	sub := &subscription{
		id: id, contract: spec, kind: kind, handler: value, scope: scope,
		view: scope.View(), label: scope.View().Label(contract), priority: opts.priority,
		sequence: b.nextSequence, once: opts.once, global: opts.global,
	}
	b.subscriptions[id] = sub
	b.mu.Unlock()

	remove := func() {
		b.mu.Lock()
		delete(b.subscriptions, id)
		b.mu.Unlock()
	}
	if err := scope.Defer(fmt.Sprintf("events subscription %d", id), func(context.Context) error {
		remove()
		return nil
	}); err != nil {
		remove()
		return nil, err
	}

	b.mu.Lock()
	current := b.subscriptions[id]
	if b.closed || current != sub {
		delete(b.subscriptions, id)
		b.mu.Unlock()
		return nil, gordis.ErrClosed
	}
	sub.active = true
	b.mu.Unlock()

	var once sync.Once
	return func() { once.Do(remove) }, nil
}

func (b *localBus) dispatch(
	ctx context.Context,
	publisher *gordis.Scope,
	contract contractSpec,
	payload any,
	kind dispatchKind,
	source *sourceAddress,
	base any,
) (any, bool, error) {
	if ctx == nil {
		return nil, false, errors.New("events: nil context")
	}
	if publisher == nil {
		return nil, false, gordis.ErrClosed
	}
	releasePublisher, err := publisher.AcquireReady()
	if err != nil {
		return nil, false, err
	}
	defer releasePublisher()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if source != nil && !publisher.Provides(source.service) {
		return nil, false, fmt.Errorf("events: current generation did not publish service %q", source.name)
	}

	labelTarget := namedTarget(contract.name)
	label := publisher.View().Label(labelTarget)
	subs, err := b.snapshot(contract, label, source, kind)
	if err != nil {
		return nil, false, err
	}
	switch kind {
	case dispatchEmit:
		return nil, false, b.emit(ctx, subs, payload)
	case dispatchParallel:
		return nil, false, b.parallelCall(ctx, subs, payload)
	case dispatchSerial:
		return b.serial(ctx, subs, payload)
	case dispatchWaterfall:
		return b.waterfall(ctx, contract, subs, payload, base)
	default:
		return nil, false, errors.New("events: invalid dispatch kind")
	}
}

type namedTarget string

func (n namedTarget) Name() string { return string(n) }

func (b *localBus) snapshot(
	contract contractSpec,
	label string,
	source *sourceAddress,
	dispatch dispatchKind,
) ([]*subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, gordis.ErrClosed
	}
	if err := b.ensureContractLocked(contract); err != nil {
		return nil, err
	}
	wanted := handlerNotification
	if dispatch == dispatchSerial {
		wanted = handlerSelection
	} else if dispatch == dispatchWaterfall {
		wanted = handlerMiddleware
	}
	var out []*subscription
	for _, sub := range b.subscriptions {
		if !sub.active || sub.kind != wanted || sub.contract.id != contract.id || sub.label != label {
			continue
		}
		if source != nil && !sub.global && sub.view.Label(namedTarget(source.name)) != source.label {
			continue
		}
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].priority != out[j].priority {
			return out[i].priority > out[j].priority
		}
		return out[i].sequence < out[j].sequence
	})
	return out, nil
}

func (b *localBus) claim(sub *subscription) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.subscriptions[sub.id] != sub || !sub.active {
		return false
	}
	if sub.once {
		delete(b.subscriptions, sub.id)
	}
	return true
}

func (b *localBus) admit(sub *subscription) (func(), bool) {
	release, err := sub.scope.AcquireReady()
	if err != nil {
		return nil, false
	}
	if !b.claim(sub) {
		release()
		return nil, false
	}
	return release, true
}

func (b *localBus) emit(ctx context.Context, subs []*subscription, payload any) error {
	for _, sub := range subs {
		if err := ctx.Err(); err != nil {
			return err
		}
		release, ok := b.admit(sub)
		if !ok {
			continue
		}
		err := callNotification(sub, ctx, payload)
		release()
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *localBus) parallelCall(ctx context.Context, subs []*subscription, payload any) error {
	errs := make([]error, len(subs))
	var wg sync.WaitGroup
	for index, sub := range subs {
		if err := ctx.Err(); err != nil {
			errs[index] = err
			break
		}
		release, ok := b.admit(sub)
		if !ok {
			continue
		}
		select {
		case b.parallel <- struct{}{}:
			if err := ctx.Err(); err != nil {
				<-b.parallel
				release()
				errs[index] = err
			}
		case <-ctx.Done():
			release()
			errs[index] = ctx.Err()
			break
		}
		if errs[index] != nil {
			break
		}
		wg.Add(1)
		go func(index int, sub *subscription, release func()) {
			defer wg.Done()
			defer func() { <-b.parallel }()
			defer release()
			errs[index] = callNotification(sub, ctx, payload)
		}(index, sub, release)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (b *localBus) serial(ctx context.Context, subs []*subscription, payload any) (any, bool, error) {
	for _, sub := range subs {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		release, ok := b.admit(sub)
		if !ok {
			continue
		}
		result, handled, err := callSelection(sub, ctx, payload)
		release()
		if err != nil || handled {
			return result, handled, err
		}
	}
	return nil, false, ErrNoHandler
}

func (b *localBus) waterfall(
	ctx context.Context,
	contract contractSpec,
	subs []*subscription,
	payload, base any,
) (any, bool, error) {
	baseValue := reflect.ValueOf(base)
	if err := inspectBase(contract, baseValue); err != nil {
		return nil, false, err
	}
	var call func(int, context.Context, any) (any, error)
	call = func(index int, callCtx context.Context, input any) (any, error) {
		if callCtx == nil {
			return nil, errors.New("events: nil context")
		}
		if err := callCtx.Err(); err != nil {
			return nil, err
		}
		for index < len(subs) {
			sub := subs[index]
			index++
			release, ok := b.admit(sub)
			if !ok {
				continue
			}
			result, err := callMiddleware(sub, callCtx, input, func(nextCtx context.Context, nextInput any) (any, error) {
				return call(index, nextCtx, nextInput)
			})
			release()
			return result, err
		}
		return callBase(baseValue, callCtx, input)
	}
	result, err := call(0, ctx, payload)
	return result, err == nil, err
}
