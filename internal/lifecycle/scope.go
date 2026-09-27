// Package lifecycle owns one local generation. It has no graph or transport dependencies.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

type cleanup struct {
	name string
	fn   func(context.Context) error
}
type task struct {
	running bool
	err     error
}

type readyTask struct {
	name string
	fn   func(context.Context) error
}

// Scope owns one generation's tasks, request leases, and cleanup callbacks.
// Do not retain it across activations. All registration methods are concurrency safe.
type Scope struct {
	id                 string
	generation         uint64
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	closed, published  bool
	quiesced           bool
	requires, provides map[string]reflect.Type
	services, staged   map[string]any
	onStop, defers     []cleanup
	tasks              map[string]*task
	afterReady         []readyTask
	work               sync.WaitGroup
	leases             int
	phase              string
	complete           bool
	residuals          []string
	err                error
	taskErr            error
	onFailure          func(error)
}

func (s *Scope) ID() string { return s.id }

func (s *Scope) Generation() uint64 { return s.generation }

func (s *Scope) Context() context.Context { return s.ctx }

// Defer registers final cleanup in reverse registration order. Always register
// immediately after acquisition and release the resource yourself on rejection.
func (s *Scope) Defer(name string, fn func(context.Context) error) error {
	return s.register(name, fn, false)
}

// OnStop closes incoming work before task cancellation and final cleanup.
func (s *Scope) OnStop(name string, fn func(context.Context) error) error {
	return s.register(name, fn, true)
}

func (s *Scope) register(name string, fn func(context.Context) error, stop bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if name == "" || fn == nil {
		return errors.New("gordis: cleanup needs a name and function")
	}
	if stop {
		s.onStop = append(s.onStop, cleanup{name, fn})
	} else {
		s.defers = append(s.defers, cleanup{name, fn})
	}
	return nil
}

// Acquire leases a scope for a business request. Stopping rejects new leases
// and waits for all existing leases. The returned release function is idempotent.
func (s *Scope) Acquire() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	return s.acquireLocked(), nil
}

// AcquireReady leases a scope only after its generation has committed Ready.
// Stopping rejects new leases and waits for all already admitted work.
func (s *Scope) AcquireReady() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if !s.published {
		return nil, ErrNotReady
	}
	return s.acquireLocked(), nil
}

func (s *Scope) acquireLocked() func() {
	s.leases++
	s.work.Add(1)
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); s.leases--; s.mu.Unlock(); s.work.Done() }) }
}

// Go runs a named managed task. A non-cancellation error or panic fails this
// generation and stops its dependent closure. A normal return is allowed.
func (s *Scope) Go(name string, fn func(context.Context) error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if name == "" || fn == nil {
		s.mu.Unlock()
		return errors.New("gordis: task needs a name and function")
	}
	if _, ok := s.tasks[name]; ok {
		s.mu.Unlock()
		return fmt.Errorf("gordis: duplicate task %q", name)
	}
	s.tasks[name] = &task{running: true}
	s.work.Add(1)
	s.mu.Unlock()
	go s.runTask(name, fn)
	return nil
}

// AfterReady registers a managed task that starts only after the generation's
// services and other Ready-gated entrances are committed. Registering after
// Ready starts it immediately. Like Go, a panic or non-cancellation error fails
// the generation.
func (s *Scope) AfterReady(name string, fn func(context.Context) error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if name == "" || fn == nil {
		s.mu.Unlock()
		return errors.New("gordis: task needs a name and function")
	}
	if _, ok := s.tasks[name]; ok {
		s.mu.Unlock()
		return fmt.Errorf("gordis: duplicate task %q", name)
	}
	s.tasks[name] = &task{}
	if !s.published {
		s.afterReady = append(s.afterReady, readyTask{name: name, fn: fn})
		s.mu.Unlock()
		return nil
	}
	s.tasks[name].running = true
	s.work.Add(1)
	s.mu.Unlock()
	go s.runTask(name, fn)
	return nil
}

func (s *Scope) startAfterReadyLocked() []readyTask {
	ready := append([]readyTask(nil), s.afterReady...)
	s.afterReady = nil
	for _, item := range ready {
		s.tasks[item.name].running = true
		s.work.Add(1)
	}
	return ready
}

func (s *Scope) runTask(name string, fn func(context.Context) error) {
	err := Invoke(func() error { return fn(s.ctx) })
	s.mu.Lock()
	if s.ctx.Err() != nil && errors.Is(err, s.ctx.Err()) {
		err = nil
	}
	t := s.tasks[name]
	t.running = false
	t.err = err
	if err != nil {
		s.taskErr = errors.Join(s.taskErr, fmt.Errorf("task %s: %w", name, err))
	}
	s.mu.Unlock()
	if err != nil && s.onFailure != nil {
		s.onFailure(fmt.Errorf("task %s: %w", name, err))
	}
	// Submit failure before releasing the task's lifetime reference, so a
	// concurrent stop cannot finish (or permit a new generation) first.
	s.work.Done()
}

func Invoke(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return fn()
}

func (s *Scope) setPhase(phase string) { s.mu.Lock(); s.phase = phase; s.mu.Unlock() }

// freeze never executes plugin code. A host freezes the entire affected set
// before running any entrance callback, even when another operation is blocked.
func (s *Scope) freeze() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.phase = "frozen"
	}
}

func (s *Scope) quiesce() {
	s.mu.Lock()
	if s.quiesced || s.complete {
		s.mu.Unlock()
		return
	}
	s.quiesced = true
	s.phase = "quiescing"
	callbacks := append([]cleanup(nil), s.onStop...)
	s.mu.Unlock()
	s.runCleanup(callbacks)
}

func (s *Scope) runCleanup(callbacks []cleanup) {
	for i := len(callbacks) - 1; i >= 0; i-- {
		c := callbacks[i]
		if err := Invoke(func() error { return c.fn(context.Background()) }); err != nil {
			s.mu.Lock()
			s.err = errors.Join(s.err, fmt.Errorf("cleanup %s: %w", c.name, err))
			s.residuals = append(s.residuals, c.name)
			s.mu.Unlock()
		}
	}
}

func (s *Scope) dispose() error {
	s.setPhase("waiting")
	s.cancel()
	s.work.Wait()
	s.setPhase("releasing")
	s.runCleanup(s.defers)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.complete = true
	s.phase = "complete"
	s.services = nil
	s.staged = nil
	return s.err
}

func (c *Controller) Snapshot() Snapshot {
	s := c.scope
	out := Snapshot{}
	s.mu.Lock()
	defer s.mu.Unlock()
	out.CleanupPhase = s.phase
	out.CleanupComplete = s.complete
	out.CleanupError = s.err
	out.Leases = s.leases
	out.Residuals = append([]string{}, s.residuals...)
	out.Tasks = []TaskSnapshot{}
	for name, t := range s.tasks {
		ts := TaskSnapshot{Name: name, Running: t.running}
		if t.err != nil {
			ts.Error = t.err.Error()
		}
		out.Tasks = append(out.Tasks, ts)
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].Name < out.Tasks[j].Name })
	return out
}
