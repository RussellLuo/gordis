package gordis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Change replaces full instance descriptions; no fields are merged. Remove also
// removes owned descendants, but never deletes consumers outside that subtree.
// Upsert of an unchanged description explicitly retries/rebuilds that instance.
type Change struct {
	Upsert []InstanceSpec
	Remove []string
	// AllowWaitingConsumers authorizes draining consumers when disabling/removing
	// a provider. Each such consumer must also opt into AllowPending.
	AllowWaitingConsumers bool
}

// Plan binds a validated candidate and its impact to this Host's revision.
// Its internals are immutable; configuration is deliberately not exported.
type Plan struct {
	host     *Host
	revision uint64
	specs    []InstanceSpec
	before   []InstanceSpec
	changed  []string
	affected []string
}

type PlanSnapshot struct {
	Revision uint64   `json:"revision"`
	Affected []string `json:"affected"`
}

func (p *Plan) Snapshot() PlanSnapshot {
	return PlanSnapshot{p.revision, append([]string{}, p.affected...)}
}

// OperationSnapshot is distinct from readiness. Completed may include pending
// instances; Failed may mean the new description is already committed. Errors
// from the upgrade and an explicit Restore remain in separate operation records.
type OperationSnapshot struct {
	ID        uint64            `json:"id"`
	Revision  uint64            `json:"revision"`
	Phase     string            `json:"phase"`
	Affected  []string          `json:"affected"`
	Versions  map[string]string `json:"versions"`
	Committed bool              `json:"committed"`
	Restores  uint64            `json:"restores,omitempty"`
	Error     string            `json:"error,omitempty"`
}

type changeRecord struct {
	status OperationSnapshot
	plan   *Plan
	op     *operation
}

func (h *Host) pluginList() []Plugin {
	plugins := make([]Plugin, 0, len(h.plugins))
	for _, spec := range h.plugins {
		plugins = append(plugins, registeredPlugin{spec: spec})
	}
	return plugins
}

// registeredPlugin lets candidate validation reuse already inspected metadata.
// The caller's registration prototype is never retained or started.
type registeredPlugin struct{ spec PluginSpec }

func (p registeredPlugin) Spec() PluginSpec { return p.spec }

func (registeredPlugin) Start(context.Context, *Scope) error {
	panic("registration prototype cannot be started")
}

// Preview validates the complete candidate before any existing scope is frozen.
// Validation callbacks run without the Host mutex and must have no side effects.
func (h *Host) Preview(change Change) (*Plan, error) {
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return nil, ErrBusy
	}
	p := &Plan{host: h, revision: h.revision}
	current := make(map[string]InstanceSpec, len(h.instances))
	edges := map[string][]string{}
	for _, id := range h.order {
		s := copySpec(h.instances[id].spec)
		current[id] = s
		p.before = append(p.before, s)
		for _, dep := range h.instances[id].prereqs {
			edges[dep] = append(edges[dep], id)
		}
	}
	changed, removed, affected := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range change.Remove {
		if _, ok := current[id]; !ok {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: unknown instance %q", id)
		}
		for _, child := range h.owned(id) {
			removed[child] = true
			changed[child] = true
		}
	}
	for _, s := range change.Upsert {
		if changed[s.ID] {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: repeated change to %q", s.ID)
		}
		changed[s.ID] = true
	}
	for id := range changed {
		if _, ok := current[id]; ok {
			for _, dep := range h.affected(id) {
				affected[dep] = true
			}
		}
	}
	// Removal/disable needs explicit permission to disrupt external consumers.
	withdrawn := map[string]bool{}
	for id := range removed {
		withdrawn[id] = true
	}
	for _, s := range change.Upsert {
		if s.Disabled {
			if _, ok := current[s.ID]; ok {
				for _, id := range h.owned(s.ID) {
					withdrawn[id] = true
				}
			}
		}
	}
	for id := range withdrawn {
		for _, dep := range h.affected(id) {
			i := h.instances[dep]
			if !withdrawn[dep] && !i.spec.Disabled && (h.retains(i) || i.state == Pending) {
				if !change.AllowWaitingConsumers || !i.spec.AllowPending {
					h.mu.Unlock()
					return nil, fmt.Errorf("gordis: change to %s requires waiting policy for consumer %s", id, dep)
				}
			}
		}
	}
	plugins := h.pluginList()
	h.mu.Unlock()
	for id := range removed {
		delete(current, id)
	}
	for _, s := range change.Upsert {
		current[s.ID] = copySpec(s)
	}
	for _, s := range current {
		p.specs = append(p.specs, s)
	}
	candidate, err := buildGraph(plugins, p.specs, true)
	if err != nil {
		return nil, err
	}
	p.specs = nil
	for _, id := range candidate.order {
		p.specs = append(p.specs, copySpec(candidate.nodes[id].spec))
	}
	for id := range changed {
		p.changed = append(p.changed, id)
		if candidate.nodes[id] != nil {
			for _, dep := range candidate.affected(id) {
				affected[dep] = true
			}
		}
		affected[id] = true
	}
	for id, i := range candidate.nodes {
		for _, dep := range i.prereqs {
			edges[dep] = append(edges[dep], id)
		}
	}
	queue := []string{}
	for id := range affected {
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, dep := range edges[id] {
			if !affected[dep] {
				affected[dep] = true
				queue = append(queue, dep)
			}
		}
	}
	for id := range affected {
		p.affected = append(p.affected, id)
	}
	sort.Strings(p.changed)
	sort.Strings(p.affected)
	return p, nil
}

// Apply revalidates a plan (including external package preflight callbacks), then
// serializes drain, graph commit and activation. ctx only bounds the wait:
// accepted work continues and can be observed with Operation/WaitOperation.
func (h *Host) Apply(ctx context.Context, p *Plan) (uint64, error) {
	return h.apply(ctx, p, 0)
}

func (h *Host) apply(ctx context.Context, p *Plan, restores uint64) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if p == nil || p.host != h {
		return 0, ErrStale
	}
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return 0, ErrBusy
	}
	if p.revision != h.revision {
		h.mu.Unlock()
		return 0, ErrStale
	}
	plugins := h.pluginList()
	h.mu.Unlock()
	candidate, err := buildGraph(plugins, p.specs, true)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return 0, ErrBusy
	}
	if p.revision != h.revision {
		h.mu.Unlock()
		return 0, ErrStale
	}
	selected := map[string]bool{}
	direct := map[string]bool{}
	for _, id := range p.changed {
		direct[id] = true
	}
	for _, id := range p.affected {
		selected[id] = true
	}
	var oldIDs []string
	for _, id := range h.order {
		if selected[id] {
			oldIDs = append(oldIDs, id)
		}
	}
	h.revision++
	h.nextOperation++
	id := h.nextOperation
	op := h.begin("change", oldIDs)
	record := &changeRecord{
		plan: p,
		op:   op,
		status: OperationSnapshot{
			ID:       id,
			Revision: h.revision,
			Phase:    "draining",
			Affected: append([]string{}, p.affected...),
			Versions: map[string]string{},
			Restores: restores,
		},
	}
	for _, spec := range p.specs {
		if selected[spec.ID] {
			record.status.Versions[spec.ID] = spec.Version
		}
	}
	h.operations[id] = record
	h.freeze(oldIDs)
	h.mu.Unlock()
	go func() {
		err := h.stopIDs(oldIDs)
		h.mu.Lock()
		for _, target := range oldIDs {
			if h.retains(h.instances[target]) {
				err = errors.Join(err, fmt.Errorf("gordis: %s retains old resources", target))
			}
		}
		if err == nil {
			// Preserve runtime identity and scopes for untouched nodes. Replace
			// the graph only after all affected old scopes are reclaimed.
			next := make(map[string]*instance, len(candidate.nodes))
			for key, node := range candidate.nodes {
				if old := h.instances[key]; old != nil {
					if selected[key] {
						fresh := newInstance(node, Stopped, old.generation, record.status.Revision)
						if old.state == Failed && !direct[key] {
							fresh.state, fresh.err, fresh.failurePhase = Failed, old.err, old.failurePhase
						}
						next[key] = fresh
					} else {
						// Untouched ready nodes keep their generation-specific bindings.
						if old.state != Ready {
							old.bindings = append([]BindingSnapshot(nil), node.bindings...)
						}
						old.graphNode = node
						old.spec = copySpec(node.spec)
						next[key] = old
					}
				} else {
					next[key] = newInstance(node, Registered, h.generations[key], record.status.Revision)
				}
			}
			h.instanceGraph, h.instances = candidate, next
			record.status.Committed = true
			record.status.Phase = "starting"
		}
		h.mu.Unlock()
		if err == nil {
			err = h.reconcile(selected)
		}
		h.mu.Lock()
		record.status.Phase = "completed"
		if err != nil {
			record.status.Phase = "failed"
			record.status.Error = err.Error()
		}
		h.mu.Unlock()
		h.finish(op, err)
	}()
	return id, wait(ctx, op)
}

func (h *Host) Operation(id uint64) (OperationSnapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.operations[id]
	if !ok {
		return OperationSnapshot{}, false
	}
	s := r.status
	s.Affected = append([]string{}, s.Affected...)
	s.Versions = map[string]string{}
	for k, v := range r.status.Versions {
		s.Versions[k] = v
	}
	return s, true
}

func (h *Host) WaitOperation(ctx context.Context, id uint64) error {
	h.mu.Lock()
	r := h.operations[id]
	h.mu.Unlock()
	if r == nil {
		return fmt.Errorf("gordis: unknown operation %d", id)
	}
	return wait(ctx, r.op)
}

// Restore explicitly reapplies the complete previous descriptions of a change.
// It is not transaction rollback. Old/new failed generations must drain first;
// validation or activation can fail again. Later management changes invalidate
// this recovery snapshot, avoiding overwriting newer user intent.
func (h *Host) Restore(ctx context.Context, operationID uint64) (uint64, error) {
	h.mu.Lock()
	r := h.operations[operationID]
	if r == nil {
		h.mu.Unlock()
		return 0, fmt.Errorf("gordis: unknown operation %d", operationID)
	}
	if h.op != nil {
		h.mu.Unlock()
		return 0, ErrBusy
	}
	if r.status.Revision != h.revision {
		h.mu.Unlock()
		return 0, ErrStale
	}
	change := Change{AllowWaitingConsumers: true}
	before := map[string]InstanceSpec{}
	for _, s := range r.plan.before {
		before[s.ID] = s
	}
	for _, id := range r.plan.changed {
		if s, ok := before[id]; ok {
			change.Upsert = append(change.Upsert, s)
		} else if h.instances[id] != nil {
			change.Remove = append(change.Remove, id)
		}
	}
	h.mu.Unlock()
	p, err := h.Preview(change)
	if err != nil {
		return 0, err
	}
	return h.apply(ctx, p, operationID)
}

// WaitReady waits for own readiness or failure. A pending instance remains
// mounted after a timeout; Snapshot identifies its blocked slots. It never
// submits a change, retries a failure, or changes desired state.
func (h *Host) WaitReady(ctx context.Context, id string) error {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		h.mu.Lock()
		i := h.instances[id]
		var err error
		ready := false
		if i == nil {
			err = fmt.Errorf("gordis: unknown instance %q", id)
		} else {
			ready = i.state == Ready
			if i.state == Failed {
				err = i.err
			}
		}
		h.mu.Unlock()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
