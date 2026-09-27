package gordis

import (
	"context"
	"errors"
	"fmt"
)

// mountNested commits a child into the operation that is currently activating
// its owner. Candidate construction and plugin validation happen without the
// graph lock; nestedMu only serializes desired-graph commits from concurrent
// activation callbacks.
func (h *Host) mountNested(ctx context.Context, env Env, spec InstanceSpec) (*Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.nestedMu.Lock()
	defer h.nestedMu.Unlock()

	h.mu.Lock()
	if err := env.validateLocked(); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	if h.op == nil || h.op != env.op {
		h.mu.Unlock()
		return nil, ErrStale
	}
	op := h.op
	if !op.addNested() {
		h.mu.Unlock()
		return nil, ErrBusy
	}
	joined := true
	defer func() {
		if joined {
			op.doneNested()
		}
	}()
	if h.instances[spec.ID] != nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("gordis: instance %q already exists under owner %q", localID(spec), spec.parent)
	}
	revision := h.revision
	plugins := h.pluginList()
	specs := make([]InstanceSpec, 0, len(h.order)+1)
	for _, id := range h.order {
		specs = append(specs, copySpec(h.instances[id].spec))
	}
	specs = append(specs, copySpec(spec))
	h.mu.Unlock()

	candidate, err := buildGraph(plugins, specs, true)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	h.mu.Lock()
	if err := env.validateLocked(); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	if h.op != op || h.revision != revision || h.instances[spec.ID] != nil {
		h.mu.Unlock()
		return nil, ErrStale
	}
	for id, node := range candidate.nodes {
		if existing := h.instances[id]; existing != nil {
			existing.graphNode = node
			existing.spec = copySpec(node.spec)
		}
	}
	h.nextLogicalID++
	record := newInstanceRecord(candidate.nodes[spec.ID], h.nextLogicalID, revision)
	h.instances[spec.ID] = record
	h.instanceGraph = candidate
	instance := &Instance{
		host: h, id: spec.ID, localID: localID(spec), logicalID: record.logicalID,
		waiterOwner: env.owner, waiterGeneration: env.ownerGeneration,
	}
	h.mu.Unlock()

	joined = false
	go func() {
		defer op.doneNested()
		_ = h.reconcile(map[string]bool{spec.ID: true})
	}()
	return instance, nil
}

func (h *Host) pruneOwnedGeneration(parentID string, generation uint64) error {
	if generation == 0 {
		return nil
	}
	h.nestedMu.Lock()
	defer h.nestedMu.Unlock()

	h.mu.Lock()
	removed := map[string]bool{}
	for _, id := range h.order {
		i := h.instances[id]
		if i == nil {
			continue
		}
		if i.spec.parent == parentID && i.spec.ownerGeneration == generation {
			for _, owned := range h.owned(id) {
				removed[owned] = true
			}
		}
	}
	if len(removed) == 0 {
		h.mu.Unlock()
		return nil
	}
	plugins := h.pluginList()
	specs := make([]InstanceSpec, 0, len(h.order)-len(removed))
	for _, id := range h.order {
		if !removed[id] {
			specs = append(specs, copySpec(h.instances[id].spec))
		}
	}
	h.mu.Unlock()

	candidate, err := buildGraph(plugins, specs, true)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range removed {
		if old := h.instances[id]; old != nil {
			h.notify(old)
			delete(h.instances, id)
		}
	}
	for id, node := range candidate.nodes {
		if existing := h.instances[id]; existing != nil {
			existing.graphNode = node
			existing.spec = copySpec(node.spec)
		}
	}
	h.instanceGraph = candidate
	return nil
}

func (h *Host) pruneFailedChildren(ids []string) error {
	type failedGeneration struct {
		id         string
		generation uint64
	}
	h.mu.Lock()
	var failed []failedGeneration
	for _, id := range ids {
		if i := h.instances[id]; i != nil && i.failed() {
			failed = append(failed, failedGeneration{id: id, generation: i.generation()})
		}
	}
	h.mu.Unlock()
	var result error
	for _, item := range failed {
		result = errors.Join(result, h.pruneOwnedGeneration(item.id, item.generation))
	}
	return result
}
