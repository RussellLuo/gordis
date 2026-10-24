// Package events adapts a Gordis event Bus to a trusted local RPC program.
// Business plugins continue to use github.com/RussellLuo/gordis/events.
package events

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

type Direction uint8

const (
	Publish Direction = 1 << iota
	Subscribe
)

// EventCodec functions must be safe for concurrent calls. Change ID whenever
// the payload representation becomes incompatible.
type EventCodec[T any] struct {
	ID     string
	Encode func(T) (json.RawMessage, error)
	Decode func(json.RawMessage) (T, error)
}

func JSONEventCodec[T any](id string) EventCodec[T] {
	return EventCodec[T]{ID: id, Encode: func(msg T) (json.RawMessage, error) { return json.Marshal(msg) }, Decode: func(raw json.RawMessage) (T, error) {
		var msg T
		t := reflect.TypeFor[T]()
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && !nullable(t) {
			return msg, ErrCodec
		}
		if err := strictJSON(raw, &msg); err != nil {
			return msg, fmt.Errorf("%w: %v", ErrCodec, err)
		}
		return msg, nil
	}}
}

func nullable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return true
	}
	return false
}

type declaration struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Input     string    `json:"input"`
	Output    string    `json:"output"`
	Direction Direction `json:"direction"`
}

// Binding is an immutable typed Topic or Hook declaration. Its zero value is invalid.
type Binding struct {
	decl         declaration
	contract     core.Contract
	encode       func(any) (json.RawMessage, error)
	decode       func(json.RawMessage) (any, error)
	on           func(*gordis.Scope, core.RemoteOptions, func(context.Context, any) error) (func(), error)
	selectOn     func(*gordis.Scope, core.RemoteOptions, func(context.Context, any) (core.RemoteResult, error)) (func(), error)
	use          func(*gordis.Scope, core.RemoteOptions, func(context.Context, any, func(context.Context, any) (any, error)) (any, error)) (func(), error)
	encodeOutput func(any) (json.RawMessage, error)
	decodeOutput func(json.RawMessage) (any, error)
	base         func(any) (func(context.Context, any) (any, error), error)
	makeBase     func(func(context.Context, any) (any, error)) any
}

func BindTopic[T any](topic core.Topic[T], codec EventCodec[T], direction Direction) Binding {
	b := Binding{decl: declaration{Kind: "topic", ID: topic.ID(), Input: codec.ID, Direction: direction}, contract: topic}
	if codec.Encode == nil || codec.Decode == nil {
		return b
	}
	b.encode = func(msg any) (raw json.RawMessage, err error) {
		defer func() {
			if p := recover(); p != nil {
				raw = nil
				err = fmt.Errorf("%w: encode panic: %v", ErrCodec, p)
			}
		}()
		value, ok := msg.(T)
		if !ok && !(msg == nil && nullable(reflect.TypeFor[T]())) {
			return nil, ErrCodec
		}
		raw, err = codec.Encode(value)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCodec, err)
		}
		if !json.Valid(raw) {
			return nil, ErrCodec
		}
		return raw, nil
	}
	b.decode = func(raw json.RawMessage) (value any, err error) {
		defer func() {
			if p := recover(); p != nil {
				value = nil
				err = fmt.Errorf("%w: decode panic: %v", ErrCodec, p)
			}
		}()
		if !json.Valid(raw) {
			return nil, ErrCodec
		}
		msg, err := codec.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCodec, err)
		}
		return msg, nil
	}
	b.on = func(scope *gordis.Scope, opts core.RemoteOptions, fn func(context.Context, any) error) (func(), error) {
		options := []core.OnOption{core.Priority(opts.Priority)}
		if opts.Once {
			options = append(options, core.Once())
		}
		if opts.Global {
			options = append(options, core.Global())
		}
		return core.Bind(scope).On(topic, func(ctx context.Context, msg T) error { return fn(ctx, msg) }, options...)
	}
	return b
}

// Bridge holds only a copied registry. Every activation creates its own peer.
type Bridge struct {
	bindings    map[string]Binding
	host, child rpc.Binding
}

func NewBridge(bindings ...Binding) (*Bridge, error) {
	if len(bindings) == 0 {
		return nil, ErrInvalidRequest
	}
	b := &Bridge{bindings: make(map[string]Binding, len(bindings))}
	decls := make([]declaration, 0, len(bindings))
	for _, binding := range bindings {
		d := binding.decl
		if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.Input) == "" || (d.Kind != "topic" && d.Kind != "hook") || d.Direction == 0 || d.Direction & ^(Publish|Subscribe) != 0 || binding.encode == nil || binding.decode == nil || (d.Kind == "topic" && binding.on == nil || d.Kind == "hook" && (strings.TrimSpace(d.Output) == "" || binding.encodeOutput == nil || binding.decodeOutput == nil || binding.selectOn == nil || binding.use == nil)) {
			return nil, ErrInvalidRequest
		}
		if _, exists := b.bindings[d.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate contract %s", ErrInvalidRequest, d.ID)
		}
		b.bindings[d.ID] = binding
		decls = append(decls, d)
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].ID < decls[j].ID })
	raw, _ := json.Marshal(decls)
	version := fmt.Sprintf("2:%x", sha256.Sum256(raw))
	b.host = serviceBinding(hostKey, version, hostMethods)
	b.child = serviceBinding(childKey, version, childMethods)
	return b, nil
}

func (b *Bridge) lookup(id string, direction Direction) (Binding, error) {
	entry, ok := b.bindings[id]
	if !ok {
		return Binding{}, ErrUndeclaredContract
	}
	if entry.decl.Direction&direction == 0 {
		return Binding{}, ErrDirection
	}
	return entry, nil
}

func (b *Bridge) local(contract core.Contract, direction Direction) (Binding, error) {
	if contract == nil {
		return Binding{}, ErrInvalidRequest
	}
	entry, err := b.lookup(contract.ID(), direction)
	if err == nil && !core.SameContract(entry.contract, contract) {
		err = ErrCodec
	}
	return entry, err
}

// BindHook registers separate input and output codecs for selection and Waterfall.
func BindHook[I, O any](hook core.Hook[I, O], input EventCodec[I], output EventCodec[O], direction Direction) Binding {
	in := BindTopic(core.NewTopic[I](hook.ID()), input, direction)
	out := BindTopic(core.NewTopic[O](hook.ID()), output, direction)
	b := Binding{decl: declaration{Kind: "hook", ID: hook.ID(), Input: input.ID, Output: output.ID, Direction: direction}, contract: hook, encode: in.encode, decode: in.decode, encodeOutput: out.encode, decodeOutput: out.decode}
	b.selectOn = func(s *gordis.Scope, opts core.RemoteOptions, fn func(context.Context, any) (core.RemoteResult, error)) (func(), error) {
		return core.Bind(s).Handle(hook, func(ctx context.Context, msg I) (O, bool, error) {
			r, err := fn(ctx, msg)
			value, typeErr := outputValue[O](r.Value)
			return value, r.Handled, errors.Join(err, typeErr)
		}, onOptions(opts)...)
	}
	b.use = func(s *gordis.Scope, opts core.RemoteOptions, fn func(context.Context, any, func(context.Context, any) (any, error)) (any, error)) (func(), error) {
		return core.Bind(s).Use(hook, func(ctx context.Context, msg I, next core.Next[I, O]) (O, error) {
			r, err := fn(ctx, msg, func(c context.Context, v any) (any, error) {
				value, err := outputValue[I](v)
				if err != nil {
					return nil, err
				}
				return next(c, value)
			})
			value, typeErr := outputValue[O](r)
			return value, errors.Join(err, typeErr)
		}, onOptions(opts)...)
	}
	b.base = func(value any) (func(context.Context, any) (any, error), error) {
		fn, ok := value.(core.Next[I, O])
		if !ok || fn == nil {
			return nil, ErrInvalidRequest
		}
		return func(ctx context.Context, msg any) (any, error) {
			v, err := outputValue[I](msg)
			if err != nil {
				return nil, err
			}
			return fn(ctx, v)
		}, nil
	}
	b.makeBase = func(fn func(context.Context, any) (any, error)) any {
		return core.Next[I, O](func(ctx context.Context, msg I) (O, error) {
			v, err := fn(ctx, msg)
			value, typeErr := outputValue[O](v)
			return value, errors.Join(err, typeErr)
		})
	}
	return b
}
func outputValue[T any](value any) (T, error) {
	var zero T
	if value == nil {
		return zero, nil
	}
	v, ok := value.(T)
	if !ok {
		return zero, ErrCodec
	}
	return v, nil
}
func onOptions(opts core.RemoteOptions) []core.OnOption {
	options := []core.OnOption{core.Priority(opts.Priority)}
	if opts.Once {
		options = append(options, core.Once())
	}
	if opts.Global {
		options = append(options, core.Global())
	}
	return options
}
func (b Binding) validKind(kind core.RemoteHandlerKind) bool {
	return b.decl.Kind == "topic" && kind == core.RemoteNotification || b.decl.Kind == "hook" && (kind == core.RemoteSelection || kind == core.RemoteMiddleware)
}
func (b Binding) validMode(mode core.RemoteDispatchMode) bool {
	return b.decl.Kind == "topic" && (mode == core.RemoteEmit || mode == core.RemoteParallel) || b.decl.Kind == "hook" && (mode == core.RemoteSerial || mode == core.RemoteWaterfall)
}
