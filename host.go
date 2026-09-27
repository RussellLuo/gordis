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

type serviceBinding struct {
	provider   string
	generation uint64
	value      any
}
type operation struct {
	done       chan struct{}
	kind       string
	targets    map[string]bool
	err        error
	nestedMu   sync.Mutex
	nestedDone chan struct{}
	nested     int
	finishing  bool
}

func (op *operation) addNested() bool {
	op.nestedMu.Lock()
	defer op.nestedMu.Unlock()
	if op.finishing && op.nested == 0 {
		return false
	}
	op.nested++
	return true
}

func (op *operation) doneNested() {
	op.nestedMu.Lock()
	op.nested--
	if op.finishing && op.nested == 0 {
		close(op.nestedDone)
	}
	op.nestedMu.Unlock()
}

func (op *operation) waitNested() {
	op.nestedMu.Lock()
	op.finishing = true
	if op.nested == 0 {
		op.nestedMu.Unlock()
		return
	}
	done := op.nestedDone
	op.nestedMu.Unlock()
	<-done
}

// Host coordinates lifecycle operations. User callbacks run without the host
// mutex. Overlapping mutations return ErrBusy; Shutdown waits for accepted work
// before draining the graph. Snapshot is safe from callbacks. Never
// synchronously wait for your own shutdown inside a callback or managed task.
type Host struct {
	*instanceGraph
	mu            sync.Mutex
	nestedMu      sync.Mutex
	cleanupMu     sync.Mutex
	instances     map[string]*instanceRecord
	services      map[string]serviceBinding
	generations   map[string]uint64
	revision      uint64
	nextOperation uint64
	nextLogicalID uint64
	// Failures freeze scopes immediately; cleanup waits for the serial executor.
	// Every entry identifies a specific generation, independently of its source.
	pending  map[string]uint64
	op       *operation
	closing  bool
	shutdown bool
}

// NewHost registers the fixed set of native Plugin types and creates an empty
// synthetic root Env. Instances are mounted through Host.Env.
func NewHost(plugins []Plugin) (*Host, error) {
	for _, plugin := range plugins {
		if plugin == nil {
			return nil, errors.New("gordis: nil plugin prototype")
		}
		if _, ok := plugin.(activator); !ok {
			return nil, errors.New("gordis: plugin prototype does not implement Activate")
		}
	}
	g, err := buildGraph(plugins, nil, true)
	if err != nil {
		return nil, err
	}
	h := &Host{
		instanceGraph: g,
		instances:     map[string]*instanceRecord{},
		services:      map[string]serviceBinding{},
		generations:   map[string]uint64{},
		pending:       map[string]uint64{},
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
	op := &operation{
		kind: kind, done: make(chan struct{}), targets: map[string]bool{},
		nestedDone: make(chan struct{}),
	}
	for _, id := range ids {
		op.targets[id] = true
	}
	h.op = op
	return op
}

func (h *Host) finish(op *operation, err error) {
	op.waitNested()
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
		if i == nil || i.generation() != gen || i.activation == nil || i.activation.scope == nil {
			continue
		}
		complete, _ := i.activation.controller.Result()
		if !complete {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		op := h.begin("stop", ids)
		go func() {
			err := h.stopIDs(ids)
			err = errors.Join(err, h.pruneFailedChildren(ids))
			h.mu.Lock()
			h.markPending()
			h.mu.Unlock()
			h.finish(op, err)
		}()
	}
}

// prerequisites checks availability; validateBindings additionally checks that
// the parent and service providers still match this activation's fixed binding.
func (h *Host) prerequisites(i *instanceRecord, validateBindings bool) error {
	if i.spec.parent != "" {
		parent := h.instances[i.spec.parent]
		if parent == nil || parent.activation == nil || (parent.phase != PhaseActivating && parent.phase != PhaseReady) {
			return fmt.Errorf("gordis: instance %s blocked by owner %s", localID(i.spec), i.spec.parent)
		}
		if i.spec.ownerGeneration != 0 && parent.generation() != i.spec.ownerGeneration {
			return fmt.Errorf("gordis: owner generation changed for %s", localID(i.spec))
		}
	}
	for _, dep := range i.deps {
		if h.instances[dep].phase != PhaseReady {
			return fmt.Errorf("gordis: instance %s blocked by %s", localID(i.spec), dep)
		}
	}
	if validateBindings && i.spec.parent != "" && h.instances[i.spec.parent].generation() != i.activation.parentGeneration {
		return fmt.Errorf("gordis: parent generation changed for %s", localID(i.spec))
	}
	for _, binding := range i.bindings() {
		if binding.Provider == "" {
			return fmt.Errorf("gordis: instance %s: missing service label %q", localID(i.spec), binding.Label)
		}
		service, ok := h.services[serviceAddress(binding.Service, binding.Label)]
		if !ok ||
			service.provider != binding.Provider ||
			service.generation != h.instances[binding.Provider].generation() ||
			(validateBindings && service.generation != binding.Generation) {
			return fmt.Errorf("gordis: instance %s: binding for service label %q is unavailable", localID(i.spec), binding.Label)
		}
	}
	return nil
}

// markPending never retries a plugin's own failure or bypasses residual cleanup.
// Called with h.mu held, after an upstream failure has finished draining.
func (h *Host) markPending() {
	for _, id := range h.order {
		i := h.instances[id]
		if !i.failed() && !h.retains(i) && h.prerequisites(i, false) != nil {
			h.setPhase(i, PhasePending)
		}
	}
}

// reconcile activates selected or pending instances in graph order after a
// startup or graph change. Runtime failures still use the Host's cleanup path.
func (h *Host) reconcile(selected map[string]bool) error {
	var result error
	for {
		progress := false
		h.mu.Lock()
		order := append([]string(nil), h.order...)
		h.mu.Unlock()
		for _, id := range order {
			h.mu.Lock()
			i := h.instances[id]
			if i == nil {
				h.mu.Unlock()
				continue
			}
			if !selected[id] && i.phase != PhasePending {
				h.mu.Unlock()
				continue
			}
			if i.phase == PhaseReady || i.failed() || h.retains(i) {
				h.mu.Unlock()
				continue
			}
			blocked := h.prerequisites(i, false)
			if blocked != nil {
				h.setPhase(i, PhasePending)
				h.mu.Unlock()
				continue
			}
			h.mu.Unlock()
			activated, err := h.activate(context.Background(), id)
			progress = progress || activated
			if err != nil {
				h.mu.Lock()
				generation := uint64(0)
				if current := h.instances[id]; current != nil {
					generation = current.generation()
				}
				affected := h.affected(id)
				h.mu.Unlock()
				result = errors.Join(result, err, h.stopIDs(affected), h.pruneOwnedGeneration(id, generation))
			}
		}
		if !progress {
			break
		}
	}
	h.mu.Lock()
	h.markPending()
	h.mu.Unlock()
	return result
}

func (h *Host) activate(ctx context.Context, id string) (bool, error) {
	h.mu.Lock()
	i := h.instances[id]
	if i.phase == PhaseReady {
		h.mu.Unlock()
		return false, nil
	}
	if i.phase == PhaseActivating {
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
	gen := h.generations[id]
	activation := &activationRecord{
		generation: gen,
		bindings:   append([]BindingSnapshot(nil), i.graphNode.bindings...),
		startDone:  make(chan struct{}),
	}
	i.activation = activation
	h.setPhase(i, PhaseActivating)
	if i.spec.parent != "" {
		activation.parentGeneration = h.instances[i.spec.parent].generation()
	}
	services := map[string]any{}
	for n := range activation.bindings {
		binding := &activation.bindings[n]
		service := h.services[serviceAddress(binding.Service, binding.Label)]
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
	s := newScope(run.Scope(), h, id, i.logicalID, gen, i.spec.view, h.op)
	activation.controller = run
	activation.scope = s
	startCtx, cancel := context.WithCancel(ctx)
	activation.startCancel = cancel
	defer cancel()
	h.mu.Unlock()
	err := invoke(func() error {
		p, err := internalplugin.Prepare(i.plugin, i.spec.Config)
		if err != nil {
			return fmt.Errorf("prepare plugin: %w", err)
		}
		return activatePlugin(startCtx, p, s)
	})
	close(activation.startDone)
	h.mu.Lock()
	activation.startCancel = nil
	var interrupted bool
	err, interrupted = run.Commit(startCtx, err, func() error {
		return h.prerequisites(i, true)
	}, func(values map[string]any) {
		for name, value := range values {
			h.services[serviceAddress(name, i.spec.providedLabels[name])] = serviceBinding{provider: id, generation: gen, value: value}
		}
		h.setPhase(i, PhaseReady)
	})
	// Upstream freezing is a stop cause, not a new failure in this consumer.
	if err != nil && !(interrupted && !i.failed()) {
		h.recordFailure(i, "activation", err)
	}
	h.mu.Unlock()
	if err != nil {
		return true, fmt.Errorf("gordis: start %s generation %d: %w", id, gen, err)
	}
	return true, nil
}

// stopAll closes all active entrances and drains consumers before providers.
// ctx only bounds this caller's wait. Cleanup keeps running after a timeout.
func (h *Host) stopAll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	if h.op != nil {
		op := h.op
		if op.kind == "stop" && len(op.targets) == len(h.instances) {
			h.mu.Unlock()
			return wait(ctx, op)
		}
		h.mu.Unlock()
		return ErrBusy
	}
	ids := append([]string(nil), h.order...)
	op := h.begin("stop", ids)
	h.revision++
	for _, target := range ids {
		i := h.instances[target]
		i.desiredRevision = h.revision
		h.notify(i)
	}
	h.freeze(ids)
	h.mu.Unlock()
	go func() { h.finish(op, h.stopIDs(ids)) }()
	return wait(ctx, op)
}

// retains is called under h.mu and includes callbacks with uncertain cleanup.
func (h *Host) retains(i *instanceRecord) bool {
	if i.activation == nil || i.activation.scope == nil {
		return false
	}
	return i.activation.controller.Retains()
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
		if i.spec.parent == id {
			out = append(out, candidate)
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
		var s *Scope
		if i.activation != nil {
			s = i.activation.scope
		}
		if s == nil {
			continue
		}
		complete, _ := i.activation.controller.Result()
		if complete {
			continue
		}
		h.setPhase(i, PhaseStopping)
		i.activation.controller.Freeze()
		if i.activation.startCancel != nil {
			i.activation.startCancel()
		}
	}
}

func (h *Host) stopIDs(ids []string) error {
	h.cleanupMu.Lock()
	defer h.cleanupMu.Unlock()
	var result error
	h.mu.Lock()
	h.freeze(ids)
	h.mu.Unlock()
	for n := len(ids) - 1; n >= 0; n-- {
		h.mu.Lock()
		i := h.instances[ids[n]]
		var started <-chan struct{}
		if i != nil && i.activation != nil {
			started = i.activation.startDone
		}
		h.mu.Unlock()
		if started != nil {
			<-started
		}
	}
	for n := len(ids) - 1; n >= 0; n-- {
		h.mu.Lock()
		i := h.instances[ids[n]]
		var run *lifecycle.Controller
		if i.activation != nil {
			run = i.activation.controller
		}
		h.mu.Unlock()
		if run != nil {
			run.Quiesce()
		}
	}
	for n := len(ids) - 1; n >= 0; n-- {
		h.mu.Lock()
		i := h.instances[ids[n]]
		var s *Scope
		if i.activation != nil {
			s = i.activation.scope
		}
		if s == nil {
			h.mu.Unlock()
			continue
		}
		if blockers := h.dependents(ids[n]); len(blockers) > 0 {
			result = errors.Join(result, fmt.Errorf("gordis: cleanup %s blocked by %s", ids[n], strings.Join(blockers, ", ")))
			h.mu.Unlock()
			continue
		}
		complete, priorErr := i.activation.controller.Result()
		h.mu.Unlock()
		if complete {
			result = errors.Join(result, priorErr)
			continue
		}
		err := i.activation.controller.Dispose()
		h.mu.Lock()
		if err != nil {
			result = errors.Join(result, fmt.Errorf("gordis: cleanup %s: %w", ids[n], err))
		}
		h.finishGeneration(i, err)
		// Failed cleanup keeps its service position reserved and pins providers.
		if err == nil {
			for name, label := range i.spec.providedLabels {
				address := serviceAddress(name, label)
				if binding, ok := h.services[address]; ok && binding.provider == ids[n] && binding.generation == i.generation() {
					delete(h.services, address)
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
	if i == nil || i.generation() != gen {
		h.mu.Unlock()
		return
	}
	h.recordFailure(i, "runtime", err)
	ids := h.affected(id)
	for _, target := range ids {
		c := h.instances[target]
		if !h.retains(c) {
			continue
		}
		activation := c.ensureActivation()
		if target != id && activation.stopReason == "" {
			activation.stopReason = fmt.Sprintf("instance %s generation %d failed", id, gen)
			h.notify(c)
		}
		h.pending[target] = c.generation()
	}
	h.freeze(ids)
	h.scheduleFailures()
	h.mu.Unlock()
}

// Snapshot returns stable copies in topological order, without configuration.
func (h *Host) Snapshot() []Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Snapshot, 0, len(h.order))
	for _, id := range h.order {
		i := h.instances[id]
		s := Snapshot{
			LocalID:         localID(i.spec),
			QualifiedID:     id,
			Plugin:          i.plugin.ID,
			Generation:      i.generation(),
			Phase:           i.phase,
			Dependencies:    append([]string{}, i.deps...),
			BlockedBy:       []string{},
			CleanupComplete: true,
			CleanupPhase:    "none",
			Residuals:       []string{},
			Tasks:           []TaskSnapshot{},
			View:            i.spec.view.snapshot(),
		}
		s.Parent = i.spec.parent
		s.Revision = i.desiredRevision
		s.BlockedLabels = []string{}
		for _, b := range i.bindings() {
			p := h.instances[b.Provider]
			if p == nil || p.phase != PhaseReady {
				s.BlockedLabels = append(s.BlockedLabels, b.Label)
			}
		}
		if i.activation != nil {
			s.ParentGeneration = i.activation.parentGeneration
			s.StopReason = i.activation.stopReason
			s.FailurePhase = i.activation.failurePhase
			s.Outcome = i.activation.outcome
			if i.activation.failure != nil {
				s.Failure = i.activation.failure.Error()
			}
			if i.activation.cleanupError != nil {
				s.CleanupError = i.activation.cleanupError.Error()
			}
		}
		s.Bindings = append([]BindingSnapshot{}, i.bindings()...)
		s.ProvidedLabels = make(map[string]string, len(i.spec.providedLabels))
		for key, label := range i.spec.providedLabels {
			s.ProvidedLabels[key] = label
		}
		for _, dep := range i.prereqs {
			if h.instances[dep].phase != PhaseReady {
				s.BlockedBy = append(s.BlockedBy, dep)
			}
		}
		if i.spec.parent != "" {
			parent := h.instances[i.spec.parent]
			if parent == nil || (parent.phase != PhaseActivating && parent.phase != PhaseReady) {
				s.BlockedBy = append(s.BlockedBy, i.spec.parent)
			}
		}
		if i.activation != nil && i.activation.scope != nil {
			local := i.activation.controller.Snapshot()
			s.CleanupPhase, s.CleanupComplete = local.CleanupPhase, local.CleanupComplete
			s.Leases, s.Residuals, s.Tasks = local.Leases, local.Residuals, local.Tasks
			if local.CleanupError != nil {
				s.CleanupError = local.CleanupError.Error()
			}
		}
		if s.CleanupPhase != "active" && !s.CleanupComplete {
			s.BlockedBy = append(s.BlockedBy, h.dependents(id)...)
		}
		out = append(out, s)
	}
	return out
}

// Shutdown permanently closes the root Env and drains every mounted instance.
// If the context expires, cleanup continues and a later Shutdown call can wait
// for it to finish.
func (h *Host) Shutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for {
		h.mu.Lock()
		if h.shutdown {
			h.mu.Unlock()
			return nil
		}
		h.closing = true
		op := h.op
		h.mu.Unlock()
		if op == nil {
			break
		}
		if err := wait(ctx, op); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	err := h.stopAll(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	h.mu.Lock()
	h.shutdown = true
	h.mu.Unlock()
	return err
}

type activator interface {
	Activate(context.Context, *Scope) error
}

func activatePlugin(ctx context.Context, p Plugin, scope *Scope) error {
	if a, ok := p.(activator); ok {
		return a.Activate(ctx, scope)
	}
	return fmt.Errorf("plugin %q does not implement Activate", p.Spec().ID)
}
