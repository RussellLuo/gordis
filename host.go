package gordis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/RussellLuo/gordis/internal/lifecycle"
	internalplugin "github.com/RussellLuo/gordis/internal/plugin"
)

type instance struct {
	*graphNode
	spec             InstanceSpec
	bindings         []BindingSnapshot
	state            State
	generation       uint64
	scope            *Scope
	lifecycle        *lifecycle.Controller
	err              error
	failurePhase     string
	stopReason       string
	parentGeneration uint64
	startCancel      context.CancelFunc
	revision         uint64
}

func newInstance(node *graphNode, state State, generation, revision uint64) *instance {
	return &instance{
		graphNode:  node,
		spec:       copySpec(node.spec),
		bindings:   append([]BindingSnapshot(nil), node.bindings...),
		state:      state,
		generation: generation,
		revision:   revision,
	}
}

type serviceBinding struct {
	provider   string
	generation uint64
	value      any
}
type operation struct {
	done    chan struct{}
	kind    string
	targets map[string]bool
	err     error
}

// Host coordinates lifecycle operations. User callbacks run without the host
// mutex. Overlapping mutations return ErrBusy; repeated stops join the current
// stop operation. Snapshot is safe from callbacks. Never synchronously wait for
// your own shutdown inside a callback or managed task.
type Host struct {
	*instanceGraph
	mu            sync.Mutex
	instances     map[string]*instance
	services      map[string]serviceBinding
	generations   map[string]uint64
	revision      uint64
	nextOperation uint64
	operations    map[uint64]*changeRecord
	// Failures freeze scopes immediately; cleanup waits for the serial executor.
	// Every entry identifies a specific generation, independently of its source.
	pending map[string]uint64
	op      *operation
}

// NewHost reads metadata from plugin prototypes, copies instance descriptions,
// then preflights configuration and validates ownership, service contracts and
// ordering before any Scope or resource is created.
func NewHost(plugins []Plugin, specs []InstanceSpec) (*Host, error) {
	g, err := buildGraph(plugins, specs, false)
	if err != nil {
		return nil, err
	}
	h := &Host{
		instanceGraph: g,
		instances:     make(map[string]*instance, len(g.nodes)),
		services:      map[string]serviceBinding{},
		generations:   map[string]uint64{},
		pending:       map[string]uint64{},
		operations:    map[uint64]*changeRecord{},
	}
	for id, node := range g.nodes {
		h.instances[id] = newInstance(node, Registered, 0, 0)
	}
	return h, nil
}

func wait(ctx context.Context, op *operation) error {
	select {
	case <-op.done:
		return op.err
	default:
	}
	select {
	case <-op.done:
		return op.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Host) begin(kind string, ids []string) *operation {
	op := &operation{kind: kind, done: make(chan struct{}), targets: map[string]bool{}}
	for _, id := range ids {
		op.targets[id] = true
	}
	h.op = op
	return op
}

func (h *Host) finish(op *operation, err error) {
	h.mu.Lock()
	op.err = err
	h.op = nil
	h.scheduleFailures()
	close(op.done)
	h.mu.Unlock()
}

// scheduleFailures is called under h.mu. Reserve the next operation before
// allowing a new activation to race with queued failure cleanup.
func (h *Host) scheduleFailures() {
	if h.op != nil {
		return
	}
	var ids []string
	for _, id := range h.order {
		gen, ok := h.pending[id]
		if !ok {
			continue
		}
		delete(h.pending, id)
		i := h.instances[id]
		if i == nil || i.generation != gen || i.scope == nil {
			continue
		}
		complete, _ := i.lifecycle.Result()
		if !complete {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		op := h.begin("stop", ids)
		go func() {
			err := h.stopIDs(ids)
			h.mu.Lock()
			h.markPending()
			h.mu.Unlock()
			h.finish(op, err)
		}()
	}
}

// Start activates the static graph in combined ownership/dependency order.
// Required startup failure rolls back this operation's new generations.
// Optional branch errors are returned while healthy parent groups remain ready.
func (h *Host) Start(ctx context.Context) error { return h.start(ctx, "") }

// StartInstance activates an existing spec and its static descendants. Parents
// and providers outside the selected subtree must already be ready.
func (h *Host) StartInstance(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("gordis: empty instance ID")
	}
	return h.start(ctx, id)
}

func (h *Host) start(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	if h.op != nil {
		h.mu.Unlock()
		return ErrBusy
	}
	ids := append([]string(nil), h.order...)
	if id != "" {
		if _, ok := h.instances[id]; !ok {
			h.mu.Unlock()
			return fmt.Errorf("gordis: unknown instance %q", id)
		}
		ids = h.owned(id)
	}
	op := h.begin("start", ids)
	for _, target := range ids {
		h.instances[target].spec.Disabled = false
	}
	h.revision++
	h.mu.Unlock()
	go func() {
		var started []string
		var err, optionalErrors error
		skipped := map[string]bool{}
		for _, id := range ids {
			if skipped[id] {
				continue
			}
			var activated bool
			activated, err = h.activate(ctx, id)
			if activated {
				started = append(started, id)
			}
			if err != nil {
				// Optional branches report their error without tearing down a
				// healthy parent. Their descendants and consumers are stopped.
				if branch := h.optionalBranch(id, op.targets); branch != "" && ctx.Err() == nil {
					affected := h.affected(branch)
					h.mu.Lock()
					for _, target := range affected {
						skipped[target] = true
						i := h.instances[target]
						if i.state != Failed && i.stopReason == "" {
							i.stopReason = fmt.Sprintf("optional branch %s failed to start", branch)
						}
					}
					h.mu.Unlock()
					optionalErrors = errors.Join(optionalErrors, err, h.stopIDs(affected))
					err = nil
					continue
				}
				break
			}
		}
		if err == nil {
			h.mu.Lock()
			err = ctx.Err()
			for _, id := range ids {
				if !skipped[id] && h.instances[id].state != Ready {
					err = errors.Join(err, fmt.Errorf("gordis: instance %s lost readiness during startup", id))
				}
			}
			h.mu.Unlock()
		}
		if err != nil {
			err = errors.Join(err, h.stopIDs(started))
		} else {
			err = h.reconcile(nil)
		}
		h.finish(op, errors.Join(err, optionalErrors))
	}()
	return wait(ctx, op)
}

func (h *Host) optionalBranch(id string, selected map[string]bool) string {
	for id != "" && selected[id] {
		i := h.instances[id]
		if i.spec.Optional {
			return id
		}
		id = i.spec.Parent
	}
	return ""
}

// prerequisites checks availability; validateBindings additionally checks that
// the parent and service providers still match this activation's fixed binding.
func (h *Host) prerequisites(i *instance, validateBindings bool) error {
	for _, dep := range i.prereqs {
		if h.instances[dep].state != Ready {
			return fmt.Errorf("gordis: instance %s blocked by %s", i.spec.ID, dep)
		}
	}
	if validateBindings && i.spec.Parent != "" && h.instances[i.spec.Parent].generation != i.parentGeneration {
		return fmt.Errorf("gordis: parent generation changed for %s", i.spec.ID)
	}
	for _, binding := range i.bindings {
		if binding.Provider == "" {
			return fmt.Errorf("gordis: instance %s: missing slot %q", i.spec.ID, binding.Slot)
		}
		service, ok := h.services[binding.Slot]
		if !ok ||
			service.provider != binding.Provider ||
			service.generation != h.instances[binding.Provider].generation ||
			(validateBindings && service.generation != binding.Generation) {
			return fmt.Errorf("gordis: instance %s: binding for slot %q is unavailable", i.spec.ID, binding.Slot)
		}
	}
	return nil
}

// markPending never retries a plugin's own failure or bypasses residual cleanup.
// Called with h.mu held, after an upstream failure has finished draining.
func (h *Host) markPending() {
	for _, id := range h.order {
		i := h.instances[id]
		if i.state != Failed && !h.disabled(i) && i.spec.AllowPending && !h.retains(i) && h.prerequisites(i, false) != nil {
			i.state = Pending
		}
	}
}

// reconcile activates selected or pending instances in graph order after a
// startup or graph change. Runtime failures still use the Host's cleanup path.
func (h *Host) reconcile(selected map[string]bool) error {
	var result error
	for _, id := range h.order {
		h.mu.Lock()
		i := h.instances[id]
		if !selected[id] && i.state != Pending {
			h.mu.Unlock()
			continue
		}
		if h.disabled(i) || i.state == Ready || i.state == Failed || h.retains(i) {
			h.mu.Unlock()
			continue
		}
		blocked := h.prerequisites(i, false)
		if blocked != nil {
			if i.spec.AllowPending {
				i.state = Pending
			} else {
				result = errors.Join(result, blocked)
			}
			h.mu.Unlock()
			continue
		}
		h.mu.Unlock()
		_, err := h.activate(context.Background(), id)
		if err != nil {
			result = errors.Join(result, err, h.stopIDs(h.affected(id)))
		}
	}
	h.mu.Lock()
	h.markPending()
	h.mu.Unlock()
	return result
}

func (h *Host) disabled(i *instance) bool {
	for i != nil {
		if i.spec.Disabled {
			return true
		}
		i = h.instances[i.spec.Parent]
	}
	return false
}

func (h *Host) activate(ctx context.Context, id string) (bool, error) {
	h.mu.Lock()
	i := h.instances[id]
	if i.state == Ready {
		h.mu.Unlock()
		return false, nil
	}
	if h.retains(i) {
		h.mu.Unlock()
		return false, fmt.Errorf("gordis: instance %s still owns resources", id)
	}
	if err := h.prerequisites(i, false); err != nil {
		h.mu.Unlock()
		return false, err
	}
	if err := ctx.Err(); err != nil {
		h.mu.Unlock()
		return false, err
	}
	h.generations[id]++
	i.generation = h.generations[id]
	gen := i.generation
	i.state = Starting
	i.err = nil
	i.failurePhase = ""
	i.stopReason = ""
	if i.spec.Parent != "" {
		i.parentGeneration = h.instances[i.spec.Parent].generation
	}
	services := map[string]any{}
	for n := range i.bindings {
		binding := &i.bindings[n]
		service := h.services[binding.Slot]
		binding.Generation = service.generation
		services[binding.Service] = service.value
	}
	run := lifecycle.New(
		id,
		gen,
		i.plugin.Requires,
		i.plugin.Provides,
		services,
		func(err error) { h.runtimeFailure(id, gen, err) },
	)
	s := run.Scope()
	i.lifecycle = run
	i.scope = s
	startCtx, cancel := context.WithCancel(ctx)
	i.startCancel = cancel
	defer cancel()
	h.mu.Unlock()
	err := invoke(func() error {
		p, err := internalplugin.Prepare(i.plugin, i.spec.Config)
		if err != nil {
			return fmt.Errorf("prepare plugin: %w", err)
		}
		return p.Start(startCtx, s)
	})
	h.mu.Lock()
	i.startCancel = nil
	var interrupted bool
	err, interrupted = run.Commit(startCtx, err, func() error {
		return h.prerequisites(i, true)
	}, func(values map[string]any) {
		for name, value := range values {
			h.services[i.spec.Outputs[name]] = serviceBinding{provider: id, generation: gen, value: value}
		}
		i.state = Ready
	})
	// Upstream freezing is a stop cause, not a new failure in this consumer.
	if err != nil && !(interrupted && i.state != Failed) {
		i.state = Failed
		i.err = errors.Join(i.err, err)
		if i.failurePhase == "" {
			i.failurePhase = "start"
		}
	}
	h.mu.Unlock()
	if err != nil {
		return true, fmt.Errorf("gordis: start %s generation %d: %w", id, gen, err)
	}
	return true, nil
}

// Stop closes all active entrances and drains consumers before providers.
// ctx only bounds this caller's wait. Cleanup keeps running after a timeout.
func (h *Host) Stop(ctx context.Context) error { return h.stop(ctx, "") }

// StopInstance stops the instance and its owned descendants. Live consumers
// outside that subtree reject the operation without changing any scope.
func (h *Host) StopInstance(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("gordis: empty instance ID")
	}
	return h.stop(ctx, id)
}

func (h *Host) stop(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	if id != "" {
		if _, ok := h.instances[id]; !ok {
			h.mu.Unlock()
			return fmt.Errorf("gordis: unknown instance %q", id)
		}
	}
	if h.op != nil {
		op := h.op
		if op.kind == "stop" && ((id == "" && len(op.targets) == len(h.instances)) || (id != "" && op.targets[id])) {
			h.mu.Unlock()
			return wait(ctx, op)
		}
		h.mu.Unlock()
		return ErrBusy
	}
	ids := append([]string(nil), h.order...)
	if id != "" {
		ids = h.owned(id)
		selected := map[string]bool{}
		for _, target := range ids {
			selected[target] = true
		}
		var blockers []string
		for _, candidate := range h.order {
			i := h.instances[candidate]
			if selected[candidate] || !h.retains(i) {
				continue
			}
			for _, dep := range i.prereqs {
				if selected[dep] {
					blockers = append(blockers, candidate)
					break
				}
			}
		}
		if len(blockers) > 0 {
			h.mu.Unlock()
			return fmt.Errorf("gordis: instance %s has active consumers: %s", id, strings.Join(blockers, ", "))
		}
	}
	op := h.begin("stop", ids)
	for _, target := range ids {
		h.instances[target].spec.Disabled = true
	}
	h.revision++
	h.freeze(ids)
	h.mu.Unlock()
	go func() { h.finish(op, h.stopIDs(ids)) }()
	return wait(ctx, op)
}

// retains is called under h.mu and includes callbacks with uncertain cleanup.
func (h *Host) retains(i *instance) bool {
	if i.scope == nil {
		return false
	}
	return i.lifecycle.Retains()
}

// dependents includes service consumers and children still owned by this
// generation. Uncertain child cleanup pins the parent just like a dependency.
func (h *Host) dependents(id string) []string {
	out := []string{}
	for _, candidate := range h.order {
		i := h.instances[candidate]
		if !h.retains(i) {
			continue
		}
		for _, dep := range i.prereqs {
			if dep == id {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}

// freeze is called under h.mu and never invokes plugin callbacks. Every scope
// rejects new work before the first potentially blocking OnStop is called.
func (h *Host) freeze(ids []string) {
	for _, id := range ids {
		i := h.instances[id]
		s := i.scope
		if s == nil {
			if i.state == Registered {
				i.state = Stopped
			}
			continue
		}
		complete, _ := i.lifecycle.Result()
		if complete {
			continue
		}
		if i.state != Failed {
			i.state = Stopping
		}
		i.lifecycle.Freeze()
		if i.startCancel != nil {
			i.startCancel()
		}
	}
}

func (h *Host) stopIDs(ids []string) error {
	var result error
	h.mu.Lock()
	h.freeze(ids)
	h.mu.Unlock()
	for n := len(ids) - 1; n >= 0; n-- {
		h.mu.Lock()
		run := h.instances[ids[n]].lifecycle
		h.mu.Unlock()
		if run != nil {
			run.Quiesce()
		}
	}
	for n := len(ids) - 1; n >= 0; n-- {
		h.mu.Lock()
		i := h.instances[ids[n]]
		s := i.scope
		if s == nil {
			h.mu.Unlock()
			continue
		}
		if blockers := h.dependents(i.spec.ID); len(blockers) > 0 {
			result = errors.Join(result, fmt.Errorf("gordis: cleanup %s blocked by %s", i.spec.ID, strings.Join(blockers, ", ")))
			h.mu.Unlock()
			continue
		}
		complete, priorErr := i.lifecycle.Result()
		h.mu.Unlock()
		if complete {
			result = errors.Join(result, priorErr)
			continue
		}
		err := i.lifecycle.Dispose()
		h.mu.Lock()
		if err != nil {
			i.err = errors.Join(i.err, err)
			if i.failurePhase == "" {
				i.failurePhase = "cleanup"
			}
			i.state = Failed
			result = errors.Join(result, fmt.Errorf("gordis: cleanup %s: %w", i.spec.ID, err))
		}
		if i.state != Failed {
			i.state = Stopped
		}
		// Failed cleanup keeps its service position reserved and pins providers.
		if err == nil {
			for _, slot := range i.spec.Outputs {
				if binding, ok := h.services[slot]; ok && binding.provider == i.spec.ID && binding.generation == i.generation {
					delete(h.services, slot)
				}
			}
		}
		h.mu.Unlock()
	}
	return result
}

func (h *Host) runtimeFailure(id string, gen uint64, err error) {
	h.mu.Lock()
	i := h.instances[id]
	if i == nil || i.generation != gen {
		h.mu.Unlock()
		return
	}
	i.err = errors.Join(i.err, err)
	i.state = Failed
	if i.failurePhase == "" {
		i.failurePhase = "runtime"
	}
	ids := h.affected(id)
	for _, target := range ids {
		c := h.instances[target]
		if !h.retains(c) {
			continue
		}
		if target != id && c.stopReason == "" {
			c.stopReason = fmt.Sprintf("instance %s generation %d failed", id, gen)
		}
		h.pending[target] = c.generation
	}
	h.freeze(ids)
	h.scheduleFailures()
	h.mu.Unlock()
}

func (h *Host) groupReady(i *instance) bool {
	if i.state != Ready {
		return false
	}
	for _, id := range i.children {
		child := h.instances[id]
		if !child.spec.Optional && (child.parentGeneration != i.generation || !h.groupReady(child)) {
			return false
		}
	}
	return true
}

// Snapshot returns stable copies in topological order, without configuration.
func (h *Host) Snapshot() []Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Snapshot, 0, len(h.order))
	for _, id := range h.order {
		i := h.instances[id]
		s := Snapshot{
			ID:              id,
			Plugin:          i.plugin.ID,
			Generation:      i.generation,
			State:           i.state,
			Dependencies:    append([]string{}, i.deps...),
			BlockedBy:       []string{},
			CleanupComplete: true,
			CleanupPhase:    "none",
			Residuals:       []string{},
			Tasks:           []TaskSnapshot{},
			FailurePhase:    i.failurePhase,
		}
		s.Parent = i.spec.Parent
		s.DesiredEnabled = !i.spec.Disabled
		s.Revision = i.revision
		s.Version = i.spec.Version
		s.BlockedSlots = []string{}
		for _, b := range i.bindings {
			p := h.instances[b.Provider]
			if p == nil || p.state != Ready {
				s.BlockedSlots = append(s.BlockedSlots, b.Slot)
			}
		}
		s.ParentGeneration = i.parentGeneration
		s.Optional = i.spec.Optional
		s.GroupReady = h.groupReady(i)
		s.StopReason = i.stopReason
		s.Bindings = append([]BindingSnapshot{}, i.bindings...)
		s.ProvidedSlots = make(map[string]string, len(i.spec.Outputs))
		for key, slot := range i.spec.Outputs {
			s.ProvidedSlots[key] = slot
		}
		if i.err != nil {
			s.LastError = i.err.Error()
		}
		for _, dep := range i.prereqs {
			if h.instances[dep].state != Ready {
				s.BlockedBy = append(s.BlockedBy, dep)
			}
		}
		if i.scope != nil {
			local := i.lifecycle.Snapshot()
			s.CleanupPhase, s.CleanupComplete = local.CleanupPhase, local.CleanupComplete
			s.Leases, s.Residuals, s.Tasks = local.Leases, local.Residuals, local.Tasks
		}
		if s.CleanupPhase != "active" && !s.CleanupComplete {
			s.BlockedBy = append(s.BlockedBy, h.dependents(id)...)
		}
		out = append(out, s)
	}
	return out
}
