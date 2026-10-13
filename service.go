package gordis

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/RussellLuo/gordis/internal/lifecycle"
)

// ErrLookupUnsupported reports that a detached/process Scope has no local Host
// for dynamic service discovery. Fixed bindings remain accessible through Get.
var ErrLookupUnsupported = errors.New("gordis: Lookup is unavailable in detached execution")

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

// Provide stages a declared service until Activate succeeds. A scope may provide
// each declared key once. Typed nils are rejected as well as nil interfaces.
func Provide[T any](s *Scope, key Key[T], value T) error {
	if s == nil || s.Scope == nil {
		return ErrClosed
	}
	return lifecycle.Provide(s.Scope, key.service, value)
}

// Get only returns services declared in Requires. References remain valid until
// this consumer's cleanup finishes; they must not escape its scope lifetime.
func Get[T any](s *Scope, key Key[T]) (T, error) {
	if s == nil || s.Scope == nil {
		var zero T
		return zero, ErrClosed
	}
	return lifecycle.Get[T](s.Scope, key.service)
}

// Lookup probes a service in this generation's fixed View without requiring a
// Requires declaration. The caller must be a current, unfrozen Host generation;
// it may call Lookup during Activate. Only Ready, unfrozen providers are visible.
// Missing, unpublished, or stopping services return the zero value, false, nil.
// Invalid keys and contract type mismatches return contextual errors. Closed
// callers return ErrClosed, superseded generations return ErrStale, and detached
// execution returns ErrLookupUnsupported.
//
// Lookup neither creates a dependency nor acquires a lease. A successful lookup
// only establishes availability at the instant of discovery: the provider may
// stop before the value is used. Old references do not follow replacements,
// become invalid automatically, or delay provider cleanup. Subsequent calls on
// an old value may succeed or fail according to the service implementation. Use
// Requires and Get when the service must survive through consumer cleanup.
func Lookup[T any](s *Scope, key Key[T]) (T, bool, error) {
	var zero T
	if s == nil || s.Scope == nil {
		return zero, false, ErrClosed
	}
	if s.host == nil {
		if lifecycle.IsClosed(s.Scope) {
			return zero, false, ErrClosed
		}
		return zero, false, ErrLookupUnsupported
	}
	h := s.host
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shutdown || h.closing {
		return zero, false, ErrClosed
	}
	caller := h.instances[s.instanceID]
	if caller == nil || caller.logicalID != s.logicalID {
		return zero, false, ErrClosed
	}
	if caller.activation == nil || caller.generation() != s.generation ||
		caller.activation.scope == nil || caller.activation.scope.Scope != s.Scope {
		return zero, false, ErrStale
	}
	if (caller.phase != PhaseActivating && caller.phase != PhaseReady) || lifecycle.IsClosed(s.Scope) {
		return zero, false, ErrClosed
	}
	name, typ := key.Name(), lifecycle.ServiceType(key.service)
	if name == "" || typ == nil {
		return zero, false, fmt.Errorf("gordis: Lookup from %s: invalid service key", s.instanceID)
	}
	label := s.view.label(name)
	address := serviceAddress(name, label)
	binding, ok := h.services[address]
	if !ok {
		return zero, false, nil
	}
	contract, ok := h.providers[address]
	if !ok || contract.id != binding.provider {
		return zero, false, nil
	}
	if contract.service.Name() != name || lifecycle.ServiceType(contract.service) != typ {
		return zero, false, fmt.Errorf(
			"gordis: Lookup from %s: service %q label %q: contract mismatch (provider %q type %v, requested type %v)",
			s.instanceID, name, label, contract.service.Name(), lifecycle.ServiceType(contract.service), typ,
		)
	}
	provider := h.instances[binding.provider]
	if provider == nil || provider.generation() != binding.generation || provider.phase != PhaseReady ||
		provider.activation == nil || provider.activation.scope == nil || lifecycle.IsClosed(provider.activation.scope.Scope) {
		return zero, false, nil
	}
	value, ok := binding.value.(T)
	if !ok {
		return zero, false, fmt.Errorf(
			"gordis: Lookup from %s: service %q label %q: value type mismatch (%T vs %v)",
			s.instanceID, name, label, binding.value, typ,
		)
	}
	return value, true, nil
}
