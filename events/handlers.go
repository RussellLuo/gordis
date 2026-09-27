package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
	boolType    = reflect.TypeOf(false)
)

func inspectHandler(spec contractSpec, handler any) (handlerKind, reflect.Value, error) {
	if handler == nil {
		return 0, reflect.Value{}, errors.New("events: nil handler")
	}
	typ := reflect.TypeOf(handler)
	value := reflect.ValueOf(handler)
	if typ.Kind() != reflect.Func || value.IsNil() || typ.NumIn() < 2 || typ.In(0) != contextType || typ.In(1) != spec.input {
		return 0, reflect.Value{}, fmt.Errorf("events: handler for %q has incompatible signature", spec.id)
	}
	if spec.kind == contractTopic && typ.NumIn() == 2 && typ.NumOut() == 1 && typ.Out(0) == errorType {
		return handlerNotification, value, nil
	}
	if spec.kind == contractHook && typ.NumIn() == 2 && typ.NumOut() == 3 &&
		typ.Out(0) == spec.output && typ.Out(1) == boolType && typ.Out(2) == errorType {
		return handlerSelection, value, nil
	}
	if spec.kind == contractHook && typ.NumIn() == 3 && typ.NumOut() == 2 &&
		typ.Out(0) == spec.output && typ.Out(1) == errorType && isNextType(typ.In(2), spec) {
		return handlerMiddleware, value, nil
	}
	return 0, reflect.Value{}, fmt.Errorf("events: handler for %q has incompatible signature", spec.id)
}

func isNextType(typ reflect.Type, spec contractSpec) bool {
	return typ.Kind() == reflect.Func && typ.NumIn() == 2 && typ.In(0) == contextType && typ.In(1) == spec.input &&
		typ.NumOut() == 2 && typ.Out(0) == spec.output && typ.Out(1) == errorType
}

func invoke(handler reflect.Value, args []reflect.Value) (out []reflect.Value, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("events: handler panic: %v", recovered)
		}
	}()
	return handler.Call(args), nil
}

func errorValue(value reflect.Value) error {
	if value.IsNil() {
		return nil
	}
	return value.Interface().(error)
}

func argument(value any, typ reflect.Type) reflect.Value {
	if value == nil {
		return reflect.Zero(typ)
	}
	return reflect.ValueOf(value)
}

func callNotification(sub *subscription, ctx context.Context, payload any) error {
	out, err := invoke(sub.handler, []reflect.Value{reflect.ValueOf(ctx), argument(payload, sub.contract.input)})
	if err != nil {
		return err
	}
	return errorValue(out[0])
}

func callSelection(sub *subscription, ctx context.Context, payload any) (any, bool, error) {
	out, err := invoke(sub.handler, []reflect.Value{reflect.ValueOf(ctx), argument(payload, sub.contract.input)})
	if err != nil {
		return nil, false, err
	}
	return out[0].Interface(), out[1].Bool(), errorValue(out[2])
}

func inspectBase(contract contractSpec, base reflect.Value) error {
	if !base.IsValid() || base.Kind() != reflect.Func || base.IsNil() {
		return errors.New("events: waterfall needs a base function")
	}
	if !isNextType(base.Type(), contract) {
		return fmt.Errorf("events: waterfall base for %q has incompatible signature", contract.id)
	}
	return nil
}

func callBase(base reflect.Value, ctx context.Context, input any) (any, error) {
	out, err := invoke(base, []reflect.Value{reflect.ValueOf(ctx), argument(input, base.Type().In(1))})
	if err != nil {
		return nil, err
	}
	return out[0].Interface(), errorValue(out[1])
}

func callMiddleware(
	sub *subscription,
	ctx context.Context,
	input any,
	next func(context.Context, any) (any, error),
) (any, error) {
	var stateMu sync.Mutex
	var active = true
	var called bool
	var nextWork sync.WaitGroup
	nextType := sub.handler.Type().In(2)
	nextValue := reflect.MakeFunc(nextType, func(args []reflect.Value) []reflect.Value {
		stateMu.Lock()
		if !active {
			stateMu.Unlock()
			return []reflect.Value{reflect.Zero(sub.contract.output), reflect.ValueOf(ErrNextExpired)}
		}
		if called {
			stateMu.Unlock()
			return []reflect.Value{reflect.Zero(sub.contract.output), reflect.ValueOf(ErrNextCalled)}
		}
		called = true
		nextWork.Add(1)
		stateMu.Unlock()
		defer nextWork.Done()
		result, err := next(args[0].Interface().(context.Context), args[1].Interface())
		resultValue := reflect.Zero(sub.contract.output)
		if result != nil {
			resultValue = reflect.ValueOf(result)
		}
		errValue := reflect.Zero(errorType)
		if err != nil {
			errValue = reflect.ValueOf(err)
		}
		return []reflect.Value{resultValue, errValue}
	})
	out, err := invoke(sub.handler, []reflect.Value{reflect.ValueOf(ctx), argument(input, sub.contract.input), nextValue})
	stateMu.Lock()
	active = false
	stateMu.Unlock()
	nextWork.Wait()
	if err != nil {
		return nil, err
	}
	return out[0].Interface(), errorValue(out[1])
}
