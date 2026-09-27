package lifecycle

import (
	"fmt"
	"reflect"
)

// ServiceSpec describes a service contract by name and exact Go type, not a
// service value. Construct it using Key.Spec for PluginSpec.Requires/Provides.
type ServiceSpec struct {
	name string
	typ  reflect.Type
}

func (s ServiceSpec) Name() string { return s.name }

// NewServiceSpec is framework-only; public callers construct specs through Key.Spec.
func NewServiceSpec(name string, typ reflect.Type) ServiceSpec {
	return ServiceSpec{name: name, typ: typ}
}

// ServiceType exposes declaration metadata only to framework packages.
func ServiceType(s ServiceSpec) reflect.Type { return s.typ }

// Provide stages a declared service until Activate succeeds. A scope may provide
// each declared key once. Typed nils are rejected as well as nil interfaces.
func Provide[T any](s *Scope, k ServiceSpec, value T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.published {
		return fmt.Errorf("gordis: services already published")
	}
	if typ, ok := s.provides[k.name]; !ok || typ != k.typ {
		return fmt.Errorf("gordis: %s has not declared provided service %q", s.id, k.name)
	}
	if _, ok := s.staged[k.name]; ok {
		return fmt.Errorf("gordis: service %q already provided", k.name)
	}
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return fmt.Errorf("gordis: nil service %q", k.name)
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return fmt.Errorf("gordis: nil service %q", k.name)
		}
	}
	s.staged[k.name] = value
	return nil
}

// Get only returns services declared in Requires. References remain valid until
// this consumer's cleanup finishes; they must not escape its scope lifetime.
func Get[T any](s *Scope, k ServiceSpec) (T, error) {
	var zero T
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return zero, ErrClosed
	}
	if typ, ok := s.requires[k.name]; !ok || typ != k.typ {
		return zero, fmt.Errorf("gordis: %s has not declared dependency %q", s.id, k.name)
	}
	v, ok := s.services[k.name].(T)
	if !ok {
		return zero, fmt.Errorf("gordis: service %q is unavailable or has the wrong type", k.name)
	}
	return v, nil
}

// Provides reports whether the Ready generation declared and committed the
// exact service contract. It is read-only support for optional routing modules.
func Provides(s *Scope, k ServiceSpec) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.published || s.provides[k.name] != k.typ {
		return false
	}
	_, ok := s.staged[k.name]
	return ok
}
