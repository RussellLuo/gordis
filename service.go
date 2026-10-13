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

// Name returns the logical service name. It is also the default visibility
// label used by Env when the key is not isolated.
func (k Key[T]) Name() string { return k.service.Name() }

// Spec returns the contract metadata used to declare a required or provided
// service. It does not create or resolve the service value.
func (k Key[T]) Spec() ServiceSpec { return k.service }
