package rpc

import (
	"errors"
	"fmt"
	"sync"

	"github.com/RussellLuo/gordis"
)

// PreparedExports owns the single prepared client set of one Attach. Reading
// clients has no factory side effects. Publication is a single attempt, even
// when a later service fails and activation must roll back.
type PreparedExports struct {
	session   *Session
	entries   []preparedExport
	mu        sync.Mutex
	attempted bool
}
type preparedExport struct {
	binding Binding
	value   any
}

func (s *Session) PrepareExports() (*PreparedExports, error) {
	if s == nil {
		return nil, gordis.ErrClosed
	}
	if s.scope != nil {
		release, err := s.scope.Acquire()
		if err != nil {
			return nil, err
		}
		defer release()
	}
	s.prepareOnce.Do(func() {
		s.mu.Lock()
		ready := s.diagnostics.Ready && !s.stopping && s.remote != nil
		s.mu.Unlock()
		if !ready {
			s.prepareErr = gordis.ErrClosed
			return
		}
		prepared := &PreparedExports{session: s}
		// Do not hold the Session lock across user factories.
		for _, b := range s.boundary.Exports {
			var value any
			err := captureFactory(func() { value = b.client(s.invoker(b)) })
			if err != nil {
				s.prepareErr = fmt.Errorf("prepare %s: %w", b.Spec().Name(), err)
				return
			}
			if !b.check(value) {
				s.prepareErr = fmt.Errorf("prepare %s: invalid contract client", b.Spec().Name())
				return
			}
			prepared.entries = append(prepared.entries, preparedExport{b, value})
		}
		s.prepared = prepared
	})
	s.mu.Lock()
	stopped := s.stopping
	s.mu.Unlock()
	if stopped {
		return nil, gordis.ErrClosed
	}
	return s.prepared, s.prepareErr
}
func captureFactory(fn func()) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("client factory panic: %v", p)
		}
	}()
	fn()
	return nil
}

func PreparedClient[T any](p *PreparedExports, key gordis.Key[T]) (T, error) {
	var zero T
	if p == nil {
		return zero, gordis.ErrClosed
	}
	for _, entry := range p.entries {
		if entry.binding.Spec() == key.Spec() {
			value, ok := entry.value.(T)
			if ok && !nilValue(value) {
				return value, nil
			}
		}
	}
	return zero, fmt.Errorf("rpc: undeclared or wrong-type export %s", key.Name())
}

func (p *PreparedExports) Provide(scope *gordis.Scope) error {
	return p.provide(scope, nil, nil)
}

// ProvideServices publishes only the selected prepared services. Other clients
// remain Session-private. Like Provide, this consumes the single publication
// attempt; selections must be unique members of the prepared export set.
func (p *PreparedExports) ProvideServices(scope *gordis.Scope, services ...gordis.ServiceSpec) error {
	selected := make(map[gordis.ServiceSpec]bool, len(services))
	var selectionErr error
	for _, service := range services {
		if selected[service] {
			selectionErr = errors.New("rpc: duplicate selected export")
		}
		selected[service] = true
	}
	return p.provide(scope, selected, selectionErr)
}

func (p *PreparedExports) provide(scope *gordis.Scope, selected map[gordis.ServiceSpec]bool, selectionErr error) error {
	if p == nil || p.session == nil || scope == nil {
		return gordis.ErrClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.attempted {
		return errors.New("rpc: exports already provided")
	}
	// Failure consumes this publication opportunity too.
	p.attempted = true
	if selectionErr != nil {
		return selectionErr
	}
	s := p.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return gordis.ErrClosed
	}
	if scope != s.scope || scope.Generation() != s.info.HostGeneration {
		return errors.New("rpc: exports belong to another Scope")
	}
	for service := range selected {
		found := false
		for _, entry := range p.entries {
			if entry.binding.Spec() == service {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("rpc: undeclared selected export %s", service.Name())
		}
	}
	for _, entry := range p.entries {
		if selected != nil && !selected[entry.binding.Spec()] {
			continue
		}
		if err := entry.binding.publish(scope, entry.value); err != nil {
			return fmt.Errorf("provide %s: %w", entry.binding.Spec().Name(), err)
		}
	}
	return nil
}
