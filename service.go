package gordis

import (
	"reflect"

	"github.com/RussellLuo/gordis/internal/lifecycle"
)

// ServiceSpec describes a service contract by name and exact Go type, not a
// service value. Construct it using Key.Spec for PluginSpec.Requires/Provides.
type ServiceSpec = lifecycle.ServiceSpec

// Key identifies a service by both its logical name and its exact Go type.
// Define shared keys in the application's public contract package.
type Key[T any] struct{ service ServiceSpec }

func NewKey[T any](name string) Key[T] {
	return Key[T]{lifecycle.NewServiceSpec(name, reflect.TypeOf((*T)(nil)).Elem())}
}

// Name returns the logical service name. Use it when mapping an InstanceSpec's
// Inputs or Outputs to deployment slots.
func (k Key[T]) Name() string { return k.service.Name() }

// Spec returns the contract metadata used to declare a required or provided
// service. It does not create or resolve the service value.
func (k Key[T]) Spec() ServiceSpec { return k.service }

// Provide stages a declared service until Start succeeds. A scope may provide
// each declared key once. Typed nils are rejected as well as nil interfaces.
func Provide[T any](s *Scope, key Key[T], value T) error {
	return lifecycle.Provide(s, key.service, value)
}

// Get only returns services declared in Requires. References remain valid until
// this consumer's cleanup finishes; they must not escape its scope lifetime.
func Get[T any](s *Scope, key Key[T]) (T, error) {
	return lifecycle.Get[T](s, key.service)
}
