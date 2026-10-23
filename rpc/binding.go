// Package gordisrpc runs declared Gordis assembly units in trusted local
// go-plugin/gRPC processes. Contracts supply codecs; the runtime knows no domains.
package gordisrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/RussellLuo/gordis"
)

// Invoker is scoped to one contract and immutable process session. Returning
// from a stream yield stops consumption and cancels the server stream.
type Invoker interface {
	Call(context.Context, string, any, any) error
	Stream(context.Context, string, any, func(json.RawMessage) error) error
}
type Unary func(context.Context, json.RawMessage) (json.RawMessage, error)
type Stream func(context.Context, json.RawMessage, func(json.RawMessage) error) error
type Handler struct {
	Methods map[string]Unary
	Streams map[string]Stream
}
type Codec[T any] struct {
	Client func(Invoker) T
	Server func(T) Handler
	// Validate checks an export after every necessary local instance is Ready.
	// It must honor initialization cancellation; Host imports use the startup
	// context too, before acquiring a process.
	Validate func(context.Context, T) error
}

// Binding is constructed from a typed key. Its codec and Go type never cross
// the process boundary; only the name, version and direction do.
type Binding struct {
	spec    gordis.ServiceSpec
	version string
	client  func(Invoker) any
	resolve func(context.Context, *gordis.Scope) (Handler, error)
	provide func(*gordis.Scope, Invoker) error
	prepare func(Invoker) (func(*gordis.Scope) error, error)
}

func NewBinding[T any](key gordis.Key[T], version string, codec Codec[T]) Binding {
	binding := Binding{spec: key.Spec(), version: version}
	if codec.Client == nil || codec.Server == nil {
		return binding
	}
	binding.client = func(invoker Invoker) any { return codec.Client(invoker) }
	binding.resolve = func(ctx context.Context, scope *gordis.Scope) (Handler, error) {
		service, err := scope.Get(key)
		if err != nil {
			return Handler{}, err
		}
		if codec.Validate != nil {
			if err = codec.Validate(ctx, service); err != nil {
				return Handler{}, err
			}
		}
		return codec.Server(service), nil
	}
	binding.prepare = func(invoker Invoker) (func(*gordis.Scope) error, error) {
		client := codec.Client(invoker)
		if nilValue(client) {
			return nil, errors.New("invalid contract client")
		}
		return func(scope *gordis.Scope) error { return scope.Provide(key, client) }, nil
	}
	binding.provide = func(scope *gordis.Scope, invoker Invoker) error { return scope.Provide(key, codec.Client(invoker)) }
	return binding
}
func (b Binding) Spec() gordis.ServiceSpec { return b.spec }
func (b Binding) Version() string          { return b.version }
func (b Binding) valid() error {
	if b.spec.Name() == "" || b.version == "" || b.client == nil || b.resolve == nil || b.provide == nil || b.prepare == nil {
		return errors.New("gordisrpc: incomplete binding")
	}
	return nil
}

// Boundary describes services crossing a child program's process boundary.
// Imports are services the child needs from the Host; Exports are services
// the child provides to the Host. Both sides use these same directions.
type Boundary struct{ Imports, Exports []Binding }
type declaration struct{ Name, Version, Direction string }

func declarations(b Boundary) ([]declaration, error) {
	var out []declaration
	for _, g := range []struct {
		direction string
		bindings  []Binding
	}{{"import", b.Imports}, {"export", b.Exports}} {
		seen := map[string]bool{}
		for _, v := range g.bindings {
			if e := v.valid(); e != nil {
				return nil, e
			}
			if seen[v.spec.Name()] {
				return nil, fmt.Errorf("duplicate %s binding %s", g.direction, v.spec.Name())
			}
			seen[v.spec.Name()] = true
			out = append(out, declaration{v.spec.Name(), v.version, g.direction})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Direction != out[j].Direction {
			return out[i].Direction < out[j].Direction
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
func RequiredServices(bindings []Binding) []gordis.ServiceSpec {
	out := make([]gordis.ServiceSpec, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.Spec())
	}
	return out
}
func ExportClient[T any](s *Session, key gordis.Key[T]) (T, error) {
	var zero T
	if s == nil || s.remote == nil {
		return zero, errors.New("gordisrpc: nil session")
	}
	for _, b := range s.boundary.Exports {
		if b.spec == key.Spec() {
			v, ok := b.client(s.invoker(b)).(T)
			if !ok || nilValue(v) {
				return zero, errors.New("invalid contract client")
			}
			return v, nil
		}
	}
	return zero, fmt.Errorf("gordisrpc: undeclared or wrong-type export %s", key.Name())
}
func nilValue(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return r.IsNil()
	}
	return false
}
