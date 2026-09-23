package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

var ErrClosed = errors.New("gordis: scope is closed")

// Controller is the framework's capability to drive one Scope. It is never
// given to plugins. The owner serializes Quiesce and Dispose and freezes all
// affected scopes before invoking callbacks. Failure callbacks must enqueue
// cleanup, never synchronously wait for the failing task itself.
type Controller struct {
	scope    *Scope
	provides []ServiceSpec
}

// New captures fixed binding values and declarations, without taking ownership
// of caller maps/slices. Identity is immutable and allocated by the owner.
func New(
	id string,
	generation uint64,
	requires, provides []ServiceSpec,
	services map[string]any,
	fail func(error),
) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scope{
		id: id, generation: generation, ctx: ctx, cancel: cancel,
		requires: map[string]reflect.Type{}, provides: map[string]reflect.Type{},
		services: map[string]any{}, staged: map[string]any{},
		tasks: map[string]*task{}, phase: "active", onFailure: fail,
	}
	for _, k := range requires {
		s.requires[k.name] = k.typ
	}
	for _, k := range provides {
		s.provides[k.name] = k.typ
	}
	for name, value := range services {
		s.services[name] = value
	}
	return &Controller{scope: s, provides: append([]ServiceSpec(nil), provides...)}
}

func (c *Controller) Scope() *Scope { return c.scope }

// Commit checks startup and staged outputs, then performs the owner's final
// validation and publication under the Scope lock. The Host must also hold its
// graph lock throughout this call, in graph -> Scope order. Neither callback
// may execute plugin code or reenter the Scope. Publication receives a copy of
// the service table, never the mutable staging map.
//
// interrupted identifies cancellation caused by freezing, before context and
// declaration checks are added. The owner decides whether this generation had
// already failed in its own right, preserving the original failure attribution.
func (c *Controller) Commit(
	ctx context.Context,
	startErr error,
	validate func() error,
	publish func(map[string]any),
) (err error, interrupted bool) {
	s := c.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	err = errors.Join(startErr, s.taskErr)
	interrupted = s.closed && (err == nil || errors.Is(err, ctx.Err()) || errors.Is(err, ErrClosed))
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && s.closed {
		err = ErrClosed
	}
	if err == nil && s.published {
		err = errors.New("gordis: services already published")
	}
	if err == nil && validate != nil {
		err = validate()
	}
	if err == nil {
		for _, k := range c.provides {
			if _, ok := s.staged[k.name]; !ok {
				err = fmt.Errorf("declared service %q was not provided", k.name)
				break
			}
		}
	}
	if err == nil {
		values := make(map[string]any, len(s.staged))
		for name, value := range s.staged {
			values[name] = value
		}
		publish(values)
		s.published = true
	}
	return err, interrupted
}

// Freeze rejects registrations and leases without running plugin code.
func (c *Controller) Freeze() { c.scope.freeze() }

// Quiesce closes entrances after the entire affected set has been frozen.
func (c *Controller) Quiesce() { c.scope.quiesce() }

// Dispose cancels tasks, drains tasks and leases, then runs final cleanup.
// The owner must first freeze and quiesce, and ensure consumers are disposed.
func (c *Controller) Dispose() error { return c.scope.dispose() }

func (c *Controller) Result() (complete bool, err error) {
	s := c.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.complete, s.err
}

func (c *Controller) Retains() bool {
	s := c.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.complete || len(s.residuals) > 0
}

type TaskSnapshot struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

// Snapshot describes local cleanup only. Graph and execution diagnostics belong
// to the owner and are deliberately absent here.
type Snapshot struct {
	CleanupPhase    string
	CleanupComplete bool
	CleanupError    error
	Residuals       []string
	Tasks           []TaskSnapshot
	Leases          int
}
