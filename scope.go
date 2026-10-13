package gordis

import (
	"context"
	"errors"
	"fmt"

	"github.com/RussellLuo/gordis/internal/lifecycle"
)

// ErrEnvUnavailable reports that an execution adapter has no Host instance
// control protocol. Such a Scope may still use services, events, and lifecycle
// resources, but cannot create a Host-invisible child graph.
var ErrEnvUnavailable = errors.New("gordis: Env is unavailable in detached execution")

// ErrLookupUnsupported reports that a detached/process Scope has no local Host
// for dynamic service discovery. Fixed bindings remain accessible through Get.
var ErrLookupUnsupported = errors.New("gordis: Lookup is unavailable in detached execution")

// Scope is the plugin-facing facade for one generation's tasks, request
// leases, and cleanup callbacks. Lifecycle control remains on the Host-owned
// controller. Do not retain a Scope across activations.
//
// The embedded type belongs to an internal package, so external applications
// can use the promoted lifecycle methods but cannot construct a live Scope.
type Scope struct {
	*lifecycle.Scope
	host       *Host
	instanceID string
	logicalID  uint64
	generation uint64
	view       View
	op         *operation
}

func newScope(
	scope *lifecycle.Scope,
	host *Host,
	instanceID string,
	logicalID uint64,
	generation uint64,
	view View,
	op *operation,
) *Scope {
	return &Scope{
		Scope: scope, host: host, instanceID: instanceID, logicalID: logicalID,
		generation: generation, view: view.clone(), op: op,
	}
}

// Env returns an immutable child-mounting view owned by this generation.
// Retaining it after the generation stops is safe: mutations are rejected as
// stale instead of being redirected to a replacement generation.
func (s *Scope) Env() Env {
	if s == nil || s.Scope == nil {
		return Env{err: ErrClosed}
	}
	if s.host == nil {
		return Env{err: ErrEnvUnavailable}
	}
	return Env{
		host: s.host, view: s.view.clone(), owner: s.instanceID,
		ownerLogicalID: s.logicalID, ownerGeneration: s.generation, op: s.op,
	}
}

// View returns a copy of this generation's fixed capability view.
func (s *Scope) View() View {
	if s == nil {
		return View{}
	}
	return s.view.clone()
}

// Provide stages a declared service until Activate succeeds. A scope may provide
// each declared key once. Typed nils are rejected as well as nil interfaces.
func (s *Scope) Provide[T any](key Key[T], value T) error {
	if s == nil || s.Scope == nil {
		return ErrClosed
	}
	return lifecycle.Provide(s.Scope, key.service, value)
}

// Get only returns services declared in Requires. References remain valid until
// this consumer's cleanup finishes; they must not escape its scope lifetime.
func (s *Scope) Get[T any](key Key[T]) (T, error) {
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
func (s *Scope) Lookup[T any](key Key[T]) (T, bool, error) {
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

// HasProvided reports whether this generation declared and committed the exact
// service. Staged values return false until Activate succeeds and publication
// commits. This checks the generation's own publication, not current service
// availability in its View. It supports events routed from a service source.
func (s *Scope) HasProvided(service ServiceSpec) bool {
	return s != nil && s.Scope != nil && lifecycle.HasProvided(s.Scope, service)
}

// AcquireReady admits one Ready-only call. The returned release function is
// idempotent. Stopping rejects new calls and waits for admitted calls to finish.
func (s *Scope) AcquireReady() (func(), error) {
	if s == nil || s.Scope == nil {
		return nil, ErrClosed
	}
	return s.Scope.AcquireReady()
}

// AfterReady registers a managed task that starts after this generation's
// Ready commit. A task error or panic fails the generation like Scope.Go.
func (s *Scope) AfterReady(name string, task func(context.Context) error) error {
	if s == nil || s.Scope == nil {
		return ErrClosed
	}
	return s.Scope.AfterReady(name, task)
}

func invoke(fn func() error) error { return lifecycle.Invoke(fn) }
