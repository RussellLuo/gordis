package gordis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// Target is a capability whose visibility can be isolated by an Env. Service
// keys implement Target; later optional modules may add other target kinds.
type Target interface {
	Name() string
}

// View is an immutable snapshot of capability visibility labels. Its zero
// value uses each target's name as the default label.
type View struct {
	labels map[string]string
}

func (v View) clone() View {
	if len(v.labels) == 0 {
		return View{}
	}
	out := View{labels: make(map[string]string, len(v.labels))}
	for target, label := range v.labels {
		out.labels[target] = label
	}
	return out
}

func (v View) label(name string) string {
	if label := v.labels[name]; label != "" {
		return label
	}
	return name
}

// Label returns the immutable visibility label for target. It is primarily
// used by optional routing modules; ordinary plugins express visibility by
// deriving an Env with Isolate.
func (v View) Label(target Target) string {
	if target == nil {
		return ""
	}
	return v.label(target.Name())
}

func (v View) snapshot() map[string]string {
	if len(v.labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(v.labels))
	for target, label := range v.labels {
		out[target] = label
	}
	return out
}

func (v View) validate() error {
	for target, label := range v.labels {
		if target == "" || label == "" {
			return errors.New("gordis: isolate target and label must not be empty")
		}
	}
	return nil
}

// Isolate returns a new View and never mutates the receiver.
func (v View) Isolate(target Target, label string) View {
	next := v.clone()
	if next.labels == nil {
		next.labels = map[string]string{}
	}
	name := ""
	if target != nil {
		name = target.Name()
	}
	next.labels[name] = label
	return next
}

// Env is an immutable, owner-relative instance control view. owner is empty
// only for the synthetic root; all other Envs are pinned to one logical owner
// and generation so a retained value cannot target a replacement activation.
type Env struct {
	host            *Host
	view            View
	owner           string
	ownerLogicalID  uint64
	ownerGeneration uint64
	root            bool
	op              *operation
	err             error
}

// Env returns the synthetic root's instance control view.
func (h *Host) Env() Env { return Env{host: h, root: true} }

// Isolate derives an Env with one capability routed to label. The original Env
// remains unchanged and no lifecycle resource is created.
func (e Env) Isolate(target Target, label string) Env {
	e.view = e.view.Isolate(target, label)
	return e
}

// Instance is a stable logical-instance handle across configuration revisions
// and generations. Unmount permanently closes the handle.
type Instance struct {
	host             *Host
	id               string
	localID          string
	logicalID        uint64
	waiterOwner      string
	waiterGeneration uint64
}

// ID returns the instance's owner-local ID.
func (i *Instance) ID() string {
	if i == nil {
		return ""
	}
	return i.localID
}

func qualifiedID(owner, local string) string {
	local = url.PathEscape(local)
	if owner == "" {
		return local
	}
	return owner + "/" + local
}

func (e Env) validateLocked() error {
	if e.err != nil {
		return e.err
	}
	if e.host == nil {
		return ErrClosed
	}
	if e.host.shutdown || e.host.closing {
		return ErrClosed
	}
	if e.root {
		return nil
	}
	owner := e.host.instances[e.owner]
	if owner == nil || owner.logicalID != e.ownerLogicalID {
		return ErrClosed
	}
	if owner.activation == nil || owner.phase == PhasePending {
		return ErrNotReady
	}
	if owner.generation() != e.ownerGeneration || owner.phase == PhaseStopping || owner.phase == PhaseStopped {
		return ErrStale
	}
	return nil
}

func (h *Host) waitIdle(ctx context.Context) error {
	for {
		h.mu.Lock()
		if h.shutdown || h.closing {
			h.mu.Unlock()
			return ErrClosed
		}
		op := h.op
		h.mu.Unlock()
		if op == nil {
			return nil
		}
		select {
		case <-op.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *Host) waitCommit(record *changeRecord) error {
	<-record.committed
	h.mu.Lock()
	err := record.commitErr
	h.mu.Unlock()
	return err
}

// Mount validates and commits an owner-relative instance, then lets activation
// converge in the background. A missing required service is a normal Pending
// state.
func (e Env) Mount(ctx context.Context, spec InstanceSpec) (*Instance, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.host == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.view.validate(); err != nil {
		return nil, err
	}
	h := e.host
	h.mu.Lock()
	if err := e.validateLocked(); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	qualified := qualifiedID(e.owner, spec.ID)
	if h.instances[qualified] != nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("gordis: instance %q already exists under owner %q", spec.ID, e.owner)
	}
	nested := !e.root && e.op != nil && h.op == e.op
	h.mu.Unlock()

	// Placement and binding data always come from the Env.
	normalized, err := normalizeMount(e, spec)
	if err != nil {
		return nil, err
	}
	if nested {
		return h.mountNested(ctx, e, normalized)
	}
	if err := h.waitIdle(ctx); err != nil {
		return nil, err
	}
	h.mu.Lock()
	if err := e.validateLocked(); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	h.mu.Unlock()
	changes := h.Changes()
	changes.Mount(e, spec)
	operation, err := changes.Apply(ctx)
	if operation == nil {
		return nil, err
	}
	// Once submit accepts the operation, ignore later caller cancellation until
	// the commit result is known. This avoids an ambiguous "maybe mounted" error.
	if err := operation.waitCommitted(context.Background()); err != nil {
		return nil, err
	}
	mounted := operation.Mounted()
	if len(mounted) != 1 {
		return nil, errors.New("gordis: committed instance is unavailable")
	}
	return mounted[0], nil
}

// MountReady mounts an instance and waits for that exact desired revision
// itself. It does not wait for ownership descendants. A timeout or activation
// failure does not unmount the returned Instance.
func (e Env) MountReady(ctx context.Context, spec InstanceSpec) (*Instance, error) {
	instance, err := e.Mount(ctx, spec)
	if err != nil {
		return nil, err
	}
	return instance, instance.WaitReady(ctx)
}

func (i *Instance) recordLocked() (*instanceRecord, error) {
	if i == nil || i.host == nil {
		return nil, ErrClosed
	}
	record := i.host.instances[i.id]
	if record == nil || record.logicalID != i.logicalID {
		return nil, ErrClosed
	}
	return record, nil
}

// Env returns a child-mounting view for the current activation. The returned
// value carries an error when the instance has no live activation; its Mount
// methods report that error without requiring a second return value here.
func (i *Instance) Env() Env {
	if i == nil || i.host == nil {
		return Env{err: ErrClosed}
	}
	h := i.host
	h.mu.Lock()
	defer h.mu.Unlock()
	record, err := i.recordLocked()
	if err != nil {
		return Env{err: err}
	}
	if record.activation == nil || (record.phase != PhaseActivating && record.phase != PhaseReady) {
		return Env{err: ErrNotReady}
	}
	return Env{
		host: h, view: record.spec.view.clone(), owner: i.id,
		ownerLogicalID: i.logicalID, ownerGeneration: record.generation(),
	}
}

func (i *Instance) replace(ctx context.Context, config json.RawMessage, update bool) error {
	if i == nil || i.host == nil {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.host.waitIdle(ctx); err != nil {
		return err
	}
	h := i.host
	changes := h.Changes()
	if update {
		changes.Update(i, config)
	} else {
		changes.Restart(i)
	}
	operation, err := changes.Apply(ctx)
	if operation == nil {
		return err
	}
	return operation.waitCommitted(context.Background())
}

// Update commits a new configuration revision and replaces the generation.
// Readiness and activation failure are observed separately with WaitReady.
func (i *Instance) Update(ctx context.Context, config json.RawMessage) error {
	return i.replace(ctx, config, true)
}

// Restart replaces the generation while preserving its configuration.
func (i *Instance) Restart(ctx context.Context) error {
	return i.replace(ctx, nil, false)
}

// WaitReady pins the current desired revision and observes only this instance,
// not its ownership descendants. A later Update or Restart makes this wait
// stale; Unmount permanently closes it.
func (i *Instance) WaitReady(ctx context.Context) error {
	if i == nil || i.host == nil {
		return ErrClosed
	}
	h := i.host
	h.mu.Lock()
	record, err := i.recordLocked()
	if err != nil {
		h.mu.Unlock()
		return err
	}
	target := revisionTarget{
		logicalID: i.logicalID, revision: record.desiredRevision,
		waiterOwner: i.waiterOwner, waiterGeneration: i.waiterGeneration,
	}
	h.mu.Unlock()
	return h.waitReadyForRevision(ctx, i.id, &target)
}

// Unmount removes the desired instance and owned subtree, then waits for cleanup.
// Surviving consumers with unresolved required bindings become Pending. Once
// accepted, cleanup continues if ctx expires; callers that need durable
// observation should use ChangeSet and retain Operation.
func (i *Instance) Unmount(ctx context.Context) error {
	if i == nil || i.host == nil {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.host.waitIdle(ctx); err != nil {
		return err
	}
	h := i.host
	h.mu.Lock()
	if _, err := i.recordLocked(); err != nil {
		h.mu.Unlock()
		return err
	}
	h.mu.Unlock()
	changes := h.Changes()
	changes.Unmount(i)
	operation, err := changes.Apply(ctx)
	if operation == nil {
		return err
	}
	if err != nil {
		return err
	}
	// Unmount's success includes completed cleanup, not only graph commit.
	return operation.Wait(ctx)
}
