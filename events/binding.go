package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/RussellLuo/gordis"
)

// Binding is the event entry point for one fixed generation.
type Binding struct{ scope *gordis.Scope }

// Bind stores scope only. Bus resolution and lifecycle validation happen on
// each On or dispatch call.
func Bind(scope *gordis.Scope) Binding { return Binding{scope: scope} }

func (e Binding) bus() (Bus, error) {
	if e.scope == nil {
		return nil, gordis.ErrClosed
	}
	return gordis.Get(e.scope, BusKey)
}

func (e Binding) subscribe(contract Contract, handler any, options ...OnOption) (func(), error) {
	bus, err := e.bus()
	if err != nil {
		return nil, err
	}
	var opts subscriptionOptions
	for _, option := range options {
		if option == nil {
			return nil, errors.New("events: nil On option")
		}
		option.apply(&opts)
	}
	return bus.subscribe(e.scope, contract, handler, opts)
}

// On registers a typed notification handler.
func (e Binding) On[T any](topic Topic[T], handler Handler[T], options ...OnOption) (func(), error) {
	return e.subscribe(topic, handler, options...)
}

// Handle registers a typed selection handler for Serial and Bail.
func (e Binding) Handle[I, O any](
	hook Hook[I, O],
	handler SelectHandler[I, O],
	options ...OnOption,
) (func(), error) {
	return e.subscribe(hook, handler, options...)
}

// Use registers typed waterfall middleware.
func (e Binding) Use[I, O any](
	hook Hook[I, O],
	middleware Middleware[I, O],
	options ...OnOption,
) (func(), error) {
	return e.subscribe(hook, middleware, options...)
}

func (e Binding) dispatch(
	ctx context.Context,
	contract contractSpec,
	payload any,
	kind dispatchKind,
	source *sourceAddress,
	base any,
) (any, bool, error) {
	bus, err := e.bus()
	if err != nil {
		return nil, false, err
	}
	return bus.dispatch(ctx, e.scope, contract, payload, kind, source, base)
}

func (e Binding) source(service gordis.ServiceSpec, target gordis.Target) (*sourceAddress, error) {
	if e.scope == nil {
		return nil, gordis.ErrClosed
	}
	if service.Name() == "" || target == nil {
		return nil, fmt.Errorf("events: invalid source service")
	}
	return &sourceAddress{
		name: service.Name(), label: e.scope.View().Label(target), service: service,
	}, nil
}

// Publish sends a notification with bounded parallel semantics.
func (e Binding) Publish[T any](ctx context.Context, topic Topic[T], payload T) error {
	return e.Parallel(ctx, topic, payload)
}

// PublishFrom sends a notification filtered by the current generation's
// declared and committed source Service address.
func (e Binding) PublishFrom[S, T any](ctx context.Context, source gordis.Key[S], topic Topic[T], payload T) error {
	address, err := e.source(source.Spec(), source)
	if err != nil {
		return err
	}
	_, _, err = e.dispatch(ctx, topic.spec, payload, dispatchParallel, address, nil)
	return err
}

// Emit invokes notification handlers in stable order and stops at the first
// error. Zero listeners succeeds.
func (e Binding) Emit[T any](ctx context.Context, topic Topic[T], payload T) error {
	_, _, err := e.dispatch(ctx, topic.spec, payload, dispatchEmit, nil, nil)
	return err
}

func (e Binding) EmitFrom[S, T any](ctx context.Context, source gordis.Key[S], topic Topic[T], payload T) error {
	address, err := e.source(source.Spec(), source)
	if err != nil {
		return err
	}
	_, _, err = e.dispatch(ctx, topic.spec, payload, dispatchEmit, address, nil)
	return err
}

// Parallel invokes all snapshot notification handlers with Bus-wide bounded
// concurrency, waits for completion, and joins their errors.
func (e Binding) Parallel[T any](ctx context.Context, topic Topic[T], payload T) error {
	_, _, err := e.dispatch(ctx, topic.spec, payload, dispatchParallel, nil, nil)
	return err
}

func (e Binding) ParallelFrom[S, T any](ctx context.Context, source gordis.Key[S], topic Topic[T], payload T) error {
	address, err := e.source(source.Spec(), source)
	if err != nil {
		return err
	}
	_, _, err = e.dispatch(ctx, topic.spec, payload, dispatchParallel, address, nil)
	return err
}

// Serial invokes selection handlers in stable order until one explicitly
// handles the input. No selected handler returns ErrNoHandler.
func (e Binding) Serial[I, O any](ctx context.Context, hook Hook[I, O], input I) (O, bool, error) {
	return runSelection(e, ctx, hook, input, nil)
}

func (e Binding) SerialFrom[S, I, O any](ctx context.Context, source gordis.Key[S], hook Hook[I, O], input I) (O, bool, error) {
	address, err := e.source(source.Spec(), source)
	if err != nil {
		var zero O
		return zero, false, err
	}
	return runSelection(e, ctx, hook, input, address)
}

// Bail is the synchronous Go alias of Serial. Both use explicit handled values;
// the separate name keeps the event contract vocabulary unambiguous.
func (e Binding) Bail[I, O any](ctx context.Context, hook Hook[I, O], input I) (O, bool, error) {
	return e.Serial(ctx, hook, input)
}

func (e Binding) BailFrom[S, I, O any](ctx context.Context, source gordis.Key[S], hook Hook[I, O], input I) (O, bool, error) {
	return e.SerialFrom(ctx, source, hook, input)
}

func runSelection[I, O any](
	e Binding,
	ctx context.Context,
	hook Hook[I, O],
	input I,
	source *sourceAddress,
) (O, bool, error) {
	result, handled, err := e.dispatch(ctx, hook.spec, input, dispatchSerial, source, nil)
	if result == nil {
		var zero O
		return zero, handled, err
	}
	value, ok := result.(O)
	if !ok {
		var zero O
		return zero, false, errors.New("events: handler returned an incompatible result")
	}
	return value, handled, err
}

// Waterfall composes middleware in stable order around base. Middleware may
// transform input, short-circuit, or invoke its Next once.
func (e Binding) Waterfall[I, O any](ctx context.Context, hook Hook[I, O], input I, base Next[I, O]) (O, error) {
	return runWaterfall(e, ctx, hook, input, nil, base)
}

func (e Binding) WaterfallFrom[S, I, O any](
	ctx context.Context,
	source gordis.Key[S],
	hook Hook[I, O],
	input I,
	base Next[I, O],
) (O, error) {
	address, err := e.source(source.Spec(), source)
	if err != nil {
		var zero O
		return zero, err
	}
	return runWaterfall(e, ctx, hook, input, address, base)
}

func runWaterfall[I, O any](
	e Binding,
	ctx context.Context,
	hook Hook[I, O],
	input I,
	source *sourceAddress,
	base Next[I, O],
) (O, error) {
	result, _, err := e.dispatch(ctx, hook.spec, input, dispatchWaterfall, source, base)
	if result == nil {
		var zero O
		return zero, err
	}
	value := reflect.ValueOf(result)
	if !value.IsValid() || value.Type() != hook.spec.output {
		var zero O
		return zero, errors.New("events: middleware returned an incompatible result")
	}
	return result.(O), err
}
