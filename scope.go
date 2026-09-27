package gordis

import (
	"context"
	"errors"

	"github.com/RussellLuo/gordis/internal/lifecycle"
)

// ErrEnvUnavailable reports that an execution adapter has no Host instance
// control protocol. Such a Scope may still use services, events, and lifecycle
// resources, but cannot create a Host-invisible child graph.
var ErrEnvUnavailable = errors.New("gordis: Env is unavailable in detached execution")

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

// Provides reports whether this Ready generation declared and committed the
// exact service. It supports optional modules that route from a service source.
func (s *Scope) Provides(service ServiceSpec) bool {
	return s != nil && s.Scope != nil && lifecycle.Provides(s.Scope, service)
}

func invoke(fn func() error) error { return lifecycle.Invoke(fn) }
