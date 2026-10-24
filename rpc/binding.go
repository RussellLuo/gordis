// Package rpc runs declared Gordis assembly units in trusted local
// go-plugin/gRPC processes. Contracts supply codecs; the runtime knows no domains.
package rpc

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
	// MethodClasses assigns unary methods to independent admission budgets in
	// Limits. NewBinding copies the map; nil/unlisted entries use Business and
	// streams must use Business. Both peers classify locally; requests cannot
	// select a class. A class change requires a new Binding version.
	// Control handlers must perform bounded management work without user business
	// execution; Continuation requires adapter-validated existing invocation work.
	MethodClasses map[string]AdmissionClass
	Client        func(Invoker) T
	Server        func(T) Handler
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
	classes map[string]AdmissionClass
	client  func(Invoker) any
	resolve func(context.Context, *gordis.Scope) (Handler, error)
	provide func(*gordis.Scope, Invoker) error
	local   func(context.Context, any) (Handler, error)
	publish func(*gordis.Scope, any) error
	check   func(any) bool
}

func NewBinding[T any](key gordis.Key[T], version string, codec Codec[T]) Binding {
	binding := Binding{spec: key.Spec(), version: version, classes: make(map[string]AdmissionClass, len(codec.MethodClasses))}
	for method, class := range codec.MethodClasses {
		binding.classes[method] = class
	}
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
		return binding.handler(codec.Server(service))
	}
	binding.check = func(value any) bool { _, ok := value.(T); return ok && !nilValue(value) }
	binding.local = func(ctx context.Context, value any) (Handler, error) {
		service, ok := value.(T)
		if !ok || nilValue(service) {
			return Handler{}, errors.New("rpc: invalid local import value")
		}
		if codec.Validate != nil {
			if err := codec.Validate(ctx, service); err != nil {
				return Handler{}, err
			}
		}
		return binding.handler(codec.Server(service))
	}
	binding.publish = func(scope *gordis.Scope, value any) error { return scope.Provide(key, value.(T)) }
	binding.provide = func(scope *gordis.Scope, invoker Invoker) error { return scope.Provide(key, codec.Client(invoker)) }
	return binding
}
func (b Binding) Spec() gordis.ServiceSpec { return b.spec }
func (b Binding) Version() string          { return b.version }
func (b Binding) valid() error {
	for method, class := range b.classes {
		if method == "" || class > Continuation {
			return errors.New("rpc: invalid method admission class")
		}
	}
	if b.spec.Name() == "" || b.version == "" || b.client == nil || b.resolve == nil || b.provide == nil || b.local == nil || b.publish == nil || b.check == nil {
		return errors.New("rpc: incomplete binding")
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

// ExportClient constructs a client for one declared export without publishing it.
// Each call invokes the client factory; object identity is not guaranteed.
// Adapters that validate and publish the same objects should use PrepareExports
// and PreparedClient instead.
func ExportClient[T any](s *Session, key gordis.Key[T]) (T, error) {
	var zero T
	if s == nil || s.remote == nil {
		return zero, errors.New("rpc: nil session")
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
	return zero, fmt.Errorf("rpc: undeclared or wrong-type export %s", key.Name())
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

// LocalImport supplies a private, activation-owned callback without a Scope
// provider. Construct it during Activate; the complete boundary still applies.
type LocalImport struct {
	binding Binding
	value   any
}

// BindLocalImport validates a value against the Binding's captured Go type.
// Attach also checks the complete boundary and uses its codec and validator.
func BindLocalImport[T any](binding Binding, value T) (LocalImport, error) {
	if err := binding.valid(); err != nil {
		return LocalImport{}, err
	}
	if !binding.check(value) {
		return LocalImport{}, errors.New("rpc: invalid local import value")
	}
	return LocalImport{binding: binding, value: value}, nil
}
