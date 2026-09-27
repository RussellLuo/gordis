package gordis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

type changeRequestKind uint8

const (
	changeMount changeRequestKind = iota
	changeUpdate
	changeRestart
	changeUnmount
)

type changeRequest struct {
	kind     changeRequestKind
	env      Env
	instance Instance
	spec     InstanceSpec
	config   json.RawMessage
}

// ChangeSet describes one immutable-candidate management change. It accepts
// only owner-relative Envs and stable Instance handles; qualified IDs and View
// internals never become a second public assembly model.
type ChangeSet struct {
	host     *Host
	requests []changeRequest
	err      error
}

// Changes begins a management change for this Host.
func (h *Host) Changes() *ChangeSet { return &ChangeSet{host: h} }

func (c *ChangeSet) append(request changeRequest) {
	if c == nil || c.err != nil {
		return
	}
	if c.host == nil {
		c.err = ErrClosed
		return
	}
	c.requests = append(c.requests, request)
}

// Mount adds an owner-relative instance to this change. The returned Instance
// becomes available from Operation.Mounted after the change commits.
func (c *ChangeSet) Mount(env Env, spec InstanceSpec) {
	c.append(changeRequest{kind: changeMount, env: env, spec: copySpec(spec)})
}

// Update replaces only an existing Instance's JSON configuration.
func (c *ChangeSet) Update(instance *Instance, config json.RawMessage) {
	request := changeRequest{kind: changeUpdate, config: append(json.RawMessage(nil), config...)}
	if instance == nil {
		if c != nil {
			c.err = ErrClosed
		}
		return
	}
	request.instance = *instance
	c.append(request)
}

// Restart replaces an existing Instance's generation without changing config.
func (c *ChangeSet) Restart(instance *Instance) {
	if instance == nil {
		if c != nil {
			c.err = ErrClosed
		}
		return
	}
	c.append(changeRequest{kind: changeRestart, instance: *instance})
}

// Unmount removes an existing Instance and its owned subtree.
func (c *ChangeSet) Unmount(instance *Instance) {
	if instance == nil {
		if c != nil {
			c.err = ErrClosed
		}
		return
	}
	c.append(changeRequest{kind: changeUnmount, instance: *instance})
}

// graphChange is the normalized Host-internal input to candidate validation.
// Public callers can only construct changes from owner-relative Env and stable
// Instance handles through ChangeSet.
type graphChange struct {
	upsert []InstanceSpec
	remove []string
}

// changePlan binds a validated candidate and its impact to this Host's
// revision. It remains internal so validation and revision guards cannot be
// separated from ChangeSet.Apply by callers.
type changePlan struct {
	host         *Host
	revision     uint64
	specs        []InstanceSpec
	affected     []string
	direct       map[string]bool
	replacements map[string]bool
	guards       []planGuard
	mounts       []planMount
	restores     []restoreEntry
}

type planGuard struct {
	env      *Env
	instance *Instance
}

type planMount struct {
	id               string
	localID          string
	waiterOwner      string
	waiterGeneration uint64
}

type restoreEntry struct {
	id          string
	before      *InstanceSpec
	env         Env
	replacement bool
	restart     bool
}

// Operation is the durable observation handle for one accepted ChangeSet.
// Apply may return this handle together with a context error; accepted work
// continues.
type Operation struct {
	host   *Host
	record *changeRecord
}

// Snapshot returns the current commit/convergence diagnostics.
func (o *Operation) Snapshot() OperationSnapshot {
	if o == nil || o.host == nil {
		return OperationSnapshot{}
	}
	o.host.mu.Lock()
	defer o.host.mu.Unlock()
	return o.host.operationSnapshotLocked(o.record)
}

// Wait waits for convergence work to finish. It is distinct from WaitReady:
// a completed operation may intentionally leave an instance Pending.
func (o *Operation) Wait(ctx context.Context) error {
	if o == nil || o.record == nil {
		return ErrClosed
	}
	return wait(ctx, o.record.op)
}

// Mounted returns newly mounted handles in ChangeSet registration order. It
// returns nil until the operation has committed successfully.
func (o *Operation) Mounted() []*Instance {
	if o == nil || o.host == nil || o.record == nil {
		return nil
	}
	o.host.mu.Lock()
	defer o.host.mu.Unlock()
	return append([]*Instance(nil), o.record.mounted...)
}

// OperationSnapshot is distinct from readiness. Completed may include pending
// instances; Failed may mean the new description is already committed. Errors
// from the upgrade and an explicit Restore remain in separate operation records.
type OperationSnapshot struct {
	ID        uint64   `json:"id"`
	Revision  uint64   `json:"revision"`
	Phase     string   `json:"phase"`
	Affected  []string `json:"affected"`
	Committed bool     `json:"committed"`
	Restores  uint64   `json:"restores,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type changeRecord struct {
	status    OperationSnapshot
	plan      *changePlan
	op        *operation
	committed chan struct{}
	commitErr error
	mounted   []*Instance
	ready     []operationReadyTarget
}

type operationReadyTarget struct {
	id     string
	target revisionTarget
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

func (registeredPlugin) Activate(context.Context, *Scope) error {
	panic("registration prototype cannot be started")
}

type requestState struct {
	mount   *changeRequest
	update  *changeRequest
	restart *changeRequest
	unmount *changeRequest
}

func normalizeMount(env Env, spec InstanceSpec) (InstanceSpec, error) {
	if err := env.view.validate(); err != nil {
		return InstanceSpec{}, err
	}
	if spec.ID == "" {
		return InstanceSpec{}, errors.New("gordis: empty instance ID")
	}
	if spec.Plugin == "" {
		return InstanceSpec{}, errors.New("gordis: empty plugin ID")
	}
	return InstanceSpec{
		ID:              qualifiedID(env.owner, spec.ID),
		Plugin:          spec.Plugin,
		Config:          append(json.RawMessage(nil), spec.Config...),
		parent:          env.owner,
		view:            env.view.clone(),
		localID:         spec.ID,
		ownerGeneration: env.ownerGeneration,
	}, nil
}

func restoreEnvLocked(h *Host, spec InstanceSpec) (Env, error) {
	if spec.parent == "" {
		return Env{host: h, view: spec.view.clone(), root: true}, nil
	}
	owner := h.instances[spec.parent]
	if owner == nil {
		return Env{}, ErrClosed
	}
	return Env{
		host: h, view: spec.view.clone(), owner: spec.parent,
		ownerLogicalID: owner.logicalID, ownerGeneration: owner.generation(),
	}, nil
}

func (p *changePlan) validateGuardsLocked() error {
	for _, guard := range p.guards {
		if guard.env != nil {
			if err := guard.env.validateLocked(); err != nil {
				return err
			}
		}
		if guard.instance != nil {
			if _, err := guard.instance.recordLocked(); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepare freezes the ChangeSet inputs and validates the complete candidate.
// It is deliberately coupled to Apply rather than exposed as an authorization
// step: candidate validation and revision guards are Host invariants.
func (c *ChangeSet) prepare() (*changePlan, error) {
	if c == nil || c.host == nil {
		return nil, ErrClosed
	}
	if c.err != nil {
		return nil, c.err
	}
	if len(c.requests) == 0 {
		return nil, errors.New("gordis: empty change set")
	}

	h := c.host
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return nil, ErrBusy
	}
	if h.shutdown || h.closing {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	revision := h.revision
	states := map[string]*requestState{}
	order := make([]string, 0, len(c.requests))
	change := graphChange{}
	guards := make([]planGuard, 0, len(c.requests))
	mounts := make([]planMount, 0, len(c.requests))
	direct := map[string]bool{}

	stateFor := func(id string) *requestState {
		state := states[id]
		if state == nil {
			state = new(requestState)
			states[id] = state
			order = append(order, id)
		}
		return state
	}

	for index := range c.requests {
		request := c.requests[index]
		switch request.kind {
		case changeMount:
			if request.env.host != h {
				h.mu.Unlock()
				return nil, ErrStale
			}
			if err := request.env.validateLocked(); err != nil {
				h.mu.Unlock()
				return nil, err
			}
			normalized, err := normalizeMount(request.env, request.spec)
			if err != nil {
				h.mu.Unlock()
				return nil, err
			}
			request.spec = normalized
			state := stateFor(normalized.ID)
			if state.mount != nil {
				h.mu.Unlock()
				return nil, fmt.Errorf("gordis: repeated mount of %q", normalized.ID)
			}
			state.mount = &request
			env := request.env
			guards = append(guards, planGuard{env: &env})
			mounts = append(mounts, planMount{
				id: normalized.ID, localID: localID(normalized),
				waiterOwner: request.env.owner, waiterGeneration: request.env.ownerGeneration,
			})
		case changeUpdate, changeRestart, changeUnmount:
			instance := request.instance
			if instance.host != h {
				h.mu.Unlock()
				return nil, ErrStale
			}
			record, err := instance.recordLocked()
			if err != nil {
				h.mu.Unlock()
				return nil, err
			}
			request.spec = copySpec(record.spec)
			state := stateFor(instance.id)
			slot := &state.update
			if request.kind == changeRestart {
				slot = &state.restart
			} else if request.kind == changeUnmount {
				slot = &state.unmount
			}
			if *slot != nil {
				h.mu.Unlock()
				return nil, fmt.Errorf("gordis: repeated change to %q", instance.id)
			}
			*slot = &request
			copy := instance
			guards = append(guards, planGuard{instance: &copy})
		default:
			h.mu.Unlock()
			return nil, errors.New("gordis: invalid change request")
		}
	}

	restores := make([]restoreEntry, 0, len(order))
	replacements := map[string]bool{}
	for _, id := range order {
		state := states[id]
		count := 0
		for _, present := range []bool{state.mount != nil, state.update != nil, state.restart != nil, state.unmount != nil} {
			if present {
				count++
			}
		}
		replacement := state.mount != nil && state.unmount != nil && count == 2
		if count > 1 && !replacement {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: conflicting changes to %q", id)
		}
		current := h.instances[id]
		if state.mount != nil && current != nil && !replacement {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: instance %q already exists", id)
		}
		if state.mount != nil && current == nil && state.unmount != nil {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: cannot replace missing instance %q", id)
		}

		restore := restoreEntry{id: id, replacement: replacement, restart: state.restart != nil}
		if current != nil {
			before := copySpec(current.spec)
			restore.before = &before
			env, err := restoreEnvLocked(h, before)
			if err != nil {
				h.mu.Unlock()
				return nil, err
			}
			restore.env = env
		}
		restores = append(restores, restore)
		direct[id] = true
		if replacement {
			replacements[id] = true
		}
		if state.unmount != nil {
			change.remove = append(change.remove, id)
		}
		switch {
		case state.mount != nil:
			change.upsert = append(change.upsert, copySpec(state.mount.spec))
		case state.update != nil:
			spec := copySpec(state.update.spec)
			spec.Config = append(json.RawMessage(nil), state.update.config...)
			change.upsert = append(change.upsert, spec)
		case state.restart != nil:
			change.upsert = append(change.upsert, copySpec(state.restart.spec))
		}
	}
	h.mu.Unlock()

	plan, err := h.preview(change)
	if err != nil {
		return nil, err
	}
	if plan.revision != revision {
		return nil, ErrStale
	}
	for _, mount := range mounts {
		for _, affected := range plan.affected {
			if mount.waiterOwner != "" && affected == mount.waiterOwner {
				return nil, fmt.Errorf("gordis: mount owner %q changes in the same change", mount.waiterOwner)
			}
		}
	}
	plan.direct = direct
	plan.replacements = replacements
	plan.guards = guards
	plan.mounts = mounts
	plan.restores = restores
	return plan, nil
}

// preview validates the complete candidate before any existing scope is
// frozen. Validation callbacks run without the Host mutex and must have no side
// effects.
func (h *Host) preview(change graphChange) (*changePlan, error) {
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return nil, ErrBusy
	}
	p := &changePlan{host: h, revision: h.revision}
	current := make(map[string]InstanceSpec, len(h.instances))
	edges := map[string][]string{}
	for _, id := range h.order {
		s := copySpec(h.instances[id].spec)
		current[id] = s
		if s.parent != "" {
			edges[s.parent] = append(edges[s.parent], id)
		}
		for _, dep := range h.instances[id].prereqs {
			edges[dep] = append(edges[dep], id)
		}
	}
	changed, removed, affected := map[string]bool{}, map[string]bool{}, map[string]bool{}
	explicitRemove := map[string]bool{}
	for _, id := range change.remove {
		if explicitRemove[id] {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: repeated removal of %q", id)
		}
		if _, ok := current[id]; !ok {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: unknown instance %q", id)
		}
		explicitRemove[id] = true
		for _, child := range h.owned(id) {
			removed[child] = true
			changed[child] = true
		}
	}
	// Children mounted through an activation Env belong to that exact owner
	// generation. Replacing the owner removes those desired entries; the new
	// generation must declare them again.
	for _, s := range change.upsert {
		if h.instances[s.ID] == nil {
			continue
		}
		for _, childID := range h.owned(s.ID) {
			if childID == s.ID || h.instances[childID].spec.ownerGeneration == 0 {
				continue
			}
			removed[childID] = true
			changed[childID] = true
		}
	}
	upserted := map[string]bool{}
	for _, s := range change.upsert {
		if upserted[s.ID] || (changed[s.ID] && !explicitRemove[s.ID]) {
			h.mu.Unlock()
			return nil, fmt.Errorf("gordis: repeated change to %q", s.ID)
		}
		upserted[s.ID] = true
		changed[s.ID] = true
	}
	for id := range changed {
		if _, ok := current[id]; ok {
			for _, dep := range h.affected(id) {
				affected[dep] = true
			}
		}
	}
	plugins := h.pluginList()
	h.mu.Unlock()
	for id := range removed {
		delete(current, id)
	}
	for _, s := range change.upsert {
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
		if candidate.nodes[id] != nil {
			for _, dep := range candidate.affected(id) {
				affected[dep] = true
			}
		}
		affected[id] = true
	}
	for id, i := range candidate.nodes {
		if i.spec.parent != "" {
			edges[i.spec.parent] = append(edges[i.spec.parent], id)
		}
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
	sort.Strings(p.affected)
	return p, nil
}

// Apply validates and atomically commits the complete candidate graph, then
// returns a durable Operation. A nil error means the candidate graph committed;
// activation may still be running or intentionally Pending. If ctx expires
// after acceptance, Operation is non-nil and remains usable for observation.
func (c *ChangeSet) Apply(ctx context.Context) (*Operation, error) {
	return c.apply(ctx, 0)
}

func (c *ChangeSet) apply(ctx context.Context, restores uint64) (*Operation, error) {
	p, err := c.prepare()
	if err != nil {
		return nil, err
	}
	_, record, err := p.host.submit(ctx, p, restores)
	if err != nil {
		return nil, err
	}
	operation := &Operation{host: p.host, record: record}
	select {
	case <-record.committed:
		p.host.mu.Lock()
		err := record.commitErr
		p.host.mu.Unlock()
		return operation, err
	default:
	}
	select {
	case <-record.committed:
		p.host.mu.Lock()
		err := record.commitErr
		p.host.mu.Unlock()
		return operation, err
	case <-ctx.Done():
		select {
		case <-record.committed:
			p.host.mu.Lock()
			err := record.commitErr
			p.host.mu.Unlock()
			return operation, err
		default:
			return operation, ctx.Err()
		}
	}
}

// submit accepts a validated graph change and starts its serialized cleanup,
// commit, and convergence work. Callers that need an unambiguous commit
// boundary can wait on record.committed independently from activation.
func (h *Host) submit(ctx context.Context, p *changePlan, restores uint64) (uint64, *changeRecord, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if p == nil || p.host != h {
		return 0, nil, ErrStale
	}
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return 0, nil, ErrBusy
	}
	if h.shutdown || h.closing {
		h.mu.Unlock()
		return 0, nil, ErrClosed
	}
	if p.revision != h.revision {
		h.mu.Unlock()
		return 0, nil, ErrStale
	}
	if err := p.validateGuardsLocked(); err != nil {
		h.mu.Unlock()
		return 0, nil, err
	}
	plugins := h.pluginList()
	h.mu.Unlock()
	candidate, err := buildGraph(plugins, p.specs, true)
	if err != nil {
		return 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return 0, nil, ErrBusy
	}
	if h.shutdown || h.closing {
		h.mu.Unlock()
		return 0, nil, ErrClosed
	}
	if p.revision != h.revision {
		h.mu.Unlock()
		return 0, nil, ErrStale
	}
	if err := p.validateGuardsLocked(); err != nil {
		h.mu.Unlock()
		return 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		h.mu.Unlock()
		return 0, nil, err
	}
	selected := map[string]bool{}
	direct := map[string]bool{}
	for id := range p.direct {
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
		plan:      p,
		op:        op,
		committed: make(chan struct{}),
		status: OperationSnapshot{
			ID:       id,
			Revision: h.revision,
			Phase:    "draining",
			Affected: append([]string{}, p.affected...),
			Restores: restores,
		},
	}
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
			// Preserve stable logical records for existing IDs. Only generation
			// records are replaced after all affected old scopes are reclaimed.
			next := make(map[string]*instanceRecord, len(candidate.nodes))
			for key, node := range candidate.nodes {
				if old := h.instances[key]; old != nil {
					if selected[key] {
						if p.replacements[key] {
							h.notify(old)
							h.nextLogicalID++
							next[key] = newInstanceRecord(node, h.nextLogicalID, record.status.Revision)
						} else {
							generation := old.generation()
							preserveFailure := old.failed() && !direct[key]
							old.graphNode = node
							old.spec = copySpec(node.spec)
							old.desiredRevision = record.status.Revision
							if !preserveFailure {
								old.activation = &activationRecord{generation: generation}
							}
							h.setPhase(old, PhaseStopped)
							h.notify(old)
							next[key] = old
						}
					} else {
						// Untouched ready nodes keep their generation-specific bindings.
						old.graphNode = node
						old.spec = copySpec(node.spec)
						next[key] = old
					}
				} else {
					h.nextLogicalID++
					next[key] = newInstanceRecord(node, h.nextLogicalID, record.status.Revision)
				}
			}
			for key, old := range h.instances {
				if next[key] == nil {
					h.notify(old)
				}
			}
			h.instanceGraph, h.instances = candidate, next
			for _, mount := range p.mounts {
				logical := h.instances[mount.id]
				if logical == nil {
					continue
				}
				record.mounted = append(record.mounted, &Instance{
					host: h, id: mount.id, localID: mount.localID, logicalID: logical.logicalID,
					waiterOwner: mount.waiterOwner, waiterGeneration: mount.waiterGeneration,
				})
			}
			for _, target := range p.affected {
				logical := h.instances[target]
				if logical == nil {
					continue
				}
				record.ready = append(record.ready, operationReadyTarget{
					id: target,
					target: revisionTarget{
						logicalID: logical.logicalID, revision: logical.desiredRevision,
					},
				})
			}
			record.status.Committed = true
			record.status.Phase = "converging"
			// Make unresolved required bindings observable as Pending at the
			// commit boundary, before activation convergence runs.
			h.markPending()
		}
		record.commitErr = err
		close(record.committed)
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
	return id, record, nil
}

func (h *Host) operationSnapshotLocked(r *changeRecord) OperationSnapshot {
	if r == nil {
		return OperationSnapshot{}
	}
	s := r.status
	// Readiness can observe an activation failure just before the convergence
	// goroutine records its final operation status. Surface that already-known
	// failure in snapshots so diagnostics do not briefly regress to an empty
	// "converging" state after WaitReady has returned the error.
	if s.Committed && s.Phase == "converging" && s.Error == "" {
		for _, target := range r.ready {
			if _, _, err := h.readyStateLocked(target.id, &target.target); err != nil {
				s.Error = err.Error()
				break
			}
		}
	}
	s.Affected = append([]string{}, s.Affected...)
	return s
}

func (o *Operation) waitCommitted(ctx context.Context) error {
	if o == nil || o.host == nil || o.record == nil {
		return ErrClosed
	}
	select {
	case <-o.record.committed:
		o.host.mu.Lock()
		err := o.record.commitErr
		o.host.mu.Unlock()
		return err
	default:
	}
	select {
	case <-o.record.committed:
		o.host.mu.Lock()
		err := o.record.commitErr
		o.host.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitReady waits for the exact logical identities and desired revisions
// created or affected by this operation. Each target is instance-local; its
// ownership descendants are not added recursively. It does not retry failures
// or silently follow a later management revision.
func (o *Operation) WaitReady(ctx context.Context) error {
	if err := o.waitCommitted(ctx); err != nil {
		return err
	}
	o.host.mu.Lock()
	targets := append([]operationReadyTarget(nil), o.record.ready...)
	o.host.mu.Unlock()
	operationDone := (<-chan struct{})(o.record.op.done)
	for {
		cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}}
		operationCase := -1
		if operationDone != nil {
			operationCase = len(cases)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(operationDone)})
		}
		allReady := true
		o.host.mu.Lock()
		for index := range targets {
			target := &targets[index]
			ready, changed, err := o.host.readyStateLocked(target.id, &target.target)
			if err != nil {
				o.host.mu.Unlock()
				return err
			}
			if !ready {
				allReady = false
			}
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(changed)})
		}
		o.host.mu.Unlock()
		if allReady {
			return nil
		}
		chosen, _, _ := reflect.Select(cases)
		switch chosen {
		case 0:
			return ctx.Err()
		case operationCase:
			if o.record.op.err != nil {
				return o.record.op.err
			}
			operationDone = nil
		}
	}
}

// Restore applies the direct pre-operation descriptions as a new operation. It
// does not revive descendants removed only because their owner generation stopped:
// a restored plugin must redeclare those children from its new Scope.Env, and an
// external Loader must reconcile them after the owner is Ready. Restore refuses to
// overwrite a later Host revision or mount through an owner Env whose generation is
// no longer current.
func (o *Operation) Restore(ctx context.Context) (*Operation, error) {
	if err := o.waitCommitted(ctx); err != nil {
		return nil, err
	}
	select {
	case <-o.record.op.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	h := o.host
	h.mu.Lock()
	if o.record.status.Revision != h.revision {
		h.mu.Unlock()
		return nil, ErrStale
	}
	entries := append([]restoreEntry(nil), o.record.plan.restores...)
	type currentEntry struct {
		restore restoreEntry
		current *Instance
	}
	current := make([]currentEntry, 0, len(entries))
	for _, entry := range entries {
		var instance *Instance
		if record := h.instances[entry.id]; record != nil {
			instance = &Instance{
				host: h, id: entry.id, localID: localID(record.spec), logicalID: record.logicalID,
			}
		}
		current = append(current, currentEntry{restore: entry, current: instance})
	}
	h.mu.Unlock()

	changes := h.Changes()
	for _, item := range current {
		entry := item.restore
		switch {
		case entry.before == nil:
			if item.current != nil {
				changes.Unmount(item.current)
			}
		case entry.replacement:
			if item.current != nil {
				changes.Unmount(item.current)
			}
			changes.Mount(entry.env, publicInstanceSpec(*entry.before))
		case item.current == nil:
			changes.Mount(entry.env, publicInstanceSpec(*entry.before))
		case entry.restart:
			changes.Restart(item.current)
		default:
			changes.Update(item.current, entry.before.Config)
		}
	}
	return changes.apply(ctx, o.record.status.ID)
}

func publicInstanceSpec(spec InstanceSpec) InstanceSpec {
	return InstanceSpec{
		ID: localID(spec), Plugin: spec.Plugin,
		Config: append(json.RawMessage(nil), spec.Config...),
	}
}
