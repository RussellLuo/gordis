package gordis

import (
	"context"
	"errors"
	"fmt"

	"github.com/RussellLuo/gordis/internal/lifecycle"
)

// activationRecord is the Host-owned bridge between the desired graph and one
// generation's graph-agnostic lifecycle controller. All fields are protected
// by Host.mu; the lifecycle controller protects its own local resource state.
type activationRecord struct {
	generation       uint64
	bindings         []BindingSnapshot
	scope            *Scope
	controller       *lifecycle.Controller
	failure          error
	failurePhase     string
	stopReason       string
	parentGeneration uint64
	startCancel      context.CancelFunc
	startDone        chan struct{}
	outcome          Outcome
	cleanupError     error
}

// instanceRecord is stable for one logical instance across configuration and
// generation replacement. changed is a close-and-replace notification channel;
// readers capture it while holding Host.mu and never poll lifecycle state.
type instanceRecord struct {
	*graphNode
	logicalID       uint64
	spec            InstanceSpec
	desiredRevision uint64
	phase           Phase
	activation      *activationRecord
	changed         chan struct{}
}

type revisionTarget struct {
	logicalID        uint64
	revision         uint64
	waiterOwner      string
	waiterGeneration uint64
}

func newInstanceRecord(node *graphNode, logicalID, revision uint64) *instanceRecord {
	return &instanceRecord{
		graphNode:       node,
		logicalID:       logicalID,
		spec:            copySpec(node.spec),
		desiredRevision: revision,
		phase:           PhaseStopped,
		changed:         make(chan struct{}),
	}
}

func (i *instanceRecord) generation() uint64 {
	if i.activation == nil {
		return 0
	}
	return i.activation.generation
}

func (i *instanceRecord) ensureActivation() *activationRecord {
	if i.activation == nil {
		i.activation = &activationRecord{}
	}
	return i.activation
}

func (i *instanceRecord) bindings() []BindingSnapshot {
	if i.activation != nil && i.activation.bindings != nil {
		return i.activation.bindings
	}
	return i.graphNode.bindings
}

func (i *instanceRecord) failed() bool {
	return i.activation != nil && i.activation.failure != nil
}

func (h *Host) notify(i *instanceRecord) {
	close(i.changed)
	i.changed = make(chan struct{})
}

func (h *Host) setPhase(i *instanceRecord, phase Phase) {
	if i.phase == phase {
		return
	}
	i.phase = phase
	h.notify(i)
}

func (h *Host) recordFailure(i *instanceRecord, phase string, err error) {
	if err == nil || i.activation == nil {
		return
	}
	if i.activation.failure == nil {
		i.activation.failure = err
		i.activation.failurePhase = phase
		h.notify(i)
	}
}

func (h *Host) finishGeneration(i *instanceRecord, cleanupErr error) {
	if i.activation == nil {
		h.setPhase(i, PhaseStopped)
		return
	}
	i.activation.cleanupError = errors.Join(i.activation.cleanupError, cleanupErr)
	local := i.activation.controller.Snapshot()
	switch {
	case i.activation.failure != nil:
		i.activation.outcome = OutcomeFailed
	case i.activation.cleanupError != nil || len(local.Residuals) != 0:
		i.activation.outcome = OutcomeIncomplete
	default:
		i.activation.outcome = OutcomeSucceeded
	}
	h.setPhase(i, PhaseStopped)
}

// waitReadyForRevision is pinned to one logical instance and exact desired
// revision through the target captured by an Instance or Operation.
func (h *Host) waitReadyForRevision(ctx context.Context, id string, target *revisionTarget) error {
	return h.waitReadyForRevisionUntil(ctx, id, target, nil)
}

func (h *Host) waitReadyForRevisionUntil(
	ctx context.Context,
	id string,
	target *revisionTarget,
	op *operation,
) error {
	var operationDone <-chan struct{}
	if op != nil {
		operationDone = op.done
	}
	for {
		h.mu.Lock()
		ready, changed, err := h.readyStateLocked(id, target)
		h.mu.Unlock()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		case <-operationDone:
			if op.err != nil {
				return op.err
			}
			operationDone = nil
		}
	}
}

func (h *Host) readyStateLocked(
	id string,
	target *revisionTarget,
) (bool, <-chan struct{}, error) {
	i := h.instances[id]
	if i == nil {
		if target != nil {
			return false, nil, ErrClosed
		}
		return false, nil, fmt.Errorf("gordis: unknown instance %q", id)
	}
	if target != nil && i.logicalID != target.logicalID {
		return false, nil, ErrClosed
	}
	if target != nil && i.desiredRevision != target.revision {
		return false, nil, ErrStale
	}
	if i.failed() {
		return false, i.changed, i.activation.failure
	}
	if target != nil && h.activationWaitCycle(i, target) {
		return false, i.changed, ErrActivationCycle
	}
	return i.phase == PhaseReady, i.changed, nil
}

func (h *Host) activationWaitCycle(target *instanceRecord, revision *revisionTarget) bool {
	if revision.waiterOwner == "" {
		return false
	}
	owner := h.instances[revision.waiterOwner]
	if owner == nil || owner.activation == nil || owner.phase != PhaseActivating ||
		owner.generation() != revision.waiterGeneration {
		return false
	}
	seen := map[string]bool{}
	var depends func(string) bool
	depends = func(id string) bool {
		if id == revision.waiterOwner {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		i := h.instances[id]
		if i == nil {
			return false
		}
		for _, dep := range i.deps {
			if depends(dep) {
				return true
			}
		}
		return false
	}
	return depends(target.spec.ID)
}
