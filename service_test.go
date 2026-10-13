package gordis_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/internal/lifecycle"
)

func lookupCaller(scopes chan<- *gordis.Scope) *testPlugin {
	return definition("lookup-caller", func(_ context.Context, s *gordis.Scope) error {
		scopes <- s
		return nil
	})
}

func lookupHost(t *testing.T, plugins ...gordis.Plugin) *gordis.Host {
	t.Helper()
	h, err := gordis.NewHost(plugins...)
	must(t, err)
	t.Cleanup(func() { must(t, h.Shutdown(deadline(t))) })
	return h
}

func assertLookup[T comparable](t *testing.T, s *gordis.Scope, key gordis.Key[T], want T, found bool) {
	t.Helper()
	value, ok, err := gordis.Lookup(s, key)
	if err != nil || ok != found || value != want {
		t.Fatalf("Lookup(%q) = %v, %v, %v; want %v, %v, nil", key.Name(), value, ok, err, want, found)
	}
}

func assertLookupError[T comparable](t *testing.T, s *gordis.Scope, key gordis.Key[T], want error) {
	t.Helper()
	value, found, err := gordis.Lookup(s, key)
	var zero T
	if value != zero || found || !errors.Is(err, want) {
		t.Fatalf("Lookup(%q) = %v, %v, %v; want zero, false, %v", key.Name(), value, found, err, want)
	}
}

func assertLookupContextError[T comparable](t *testing.T, s *gordis.Scope, key gordis.Key[T], message string) {
	t.Helper()
	value, found, err := gordis.Lookup(s, key)
	var zero T
	if value != zero || found || err == nil || !strings.Contains(err.Error(), message) ||
		!strings.Contains(err.Error(), s.ID()) || !strings.Contains(err.Error(), key.Name()) {
		t.Fatalf("Lookup(%q) = %v, %v, %v; want contextual %q error", key.Name(), value, found, err, message)
	}
}

func TestLookupOptionalFallbackAndProviderChanges(t *testing.T) {
	scopes := make(chan *gordis.Scope, 2)
	var starts atomic.Int32
	caller := lookupCaller(scopes)
	caller.Requires = []gordis.ServiceSpec{p2ValueKey.Spec()}
	caller.ActivateFunc = func(_ context.Context, s *gordis.Scope) error {
		starts.Add(1)
		scopes <- s
		return nil
	}
	provider := definition("cache", func(_ context.Context, s *gordis.Scope) error {
		value := int(s.Generation())
		return gordis.Provide(s, valueKey, &value)
	})
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	h := lookupHost(t, caller, provider, new(p2Provider))
	_, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "database", Plugin: "p2-provider", Config: []byte(`{"value":"database"}`),
	})
	must(t, err)
	_, err = h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "caller", Plugin: "lookup-caller"})
	must(t, err)
	s := recv(t, scopes)
	initial := find(h, "caller")
	database, err := gordis.Get(s, p2ValueKey)
	must(t, err)
	read := func() string {
		cache, found, err := gordis.Lookup(s, valueKey)
		must(t, err)
		if found {
			return fmt.Sprintf("cache:%d", *cache)
		}
		return database.Value()
	}
	unchanged := func() {
		t.Helper()
		snapshot := find(h, "caller")
		if snapshot.Phase != gordis.PhaseReady || snapshot.Generation != initial.Generation ||
			snapshot.Revision != initial.Revision || starts.Load() != 1 ||
			len(snapshot.Dependencies) != 1 || snapshot.Dependencies[0] != "database" ||
			len(snapshot.Bindings) != 1 || snapshot.Bindings[0] != initial.Bindings[0] {
			t.Fatalf("optional lookup changed caller or its fixed binding: %+v", snapshot)
		}
	}
	if got := read(); got != "database" {
		t.Fatal(got)
	}
	unchanged()
	if _, err := gordis.Get(s, valueKey); err == nil || !strings.Contains(err.Error(), "has not declared dependency") {
		t.Fatalf("undeclared Get = %v", err)
	}
	p, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "cache", Plugin: "cache"})
	must(t, err)
	old, found, err := gordis.Lookup(s, valueKey)
	if err != nil || !found || old == nil || read() != "cache:1" {
		t.Fatalf("cache appearance: %v, %v, %v", old, found, err)
	}
	if value, err := gordis.Get(s, valueKey); value != nil || err == nil || !strings.Contains(err.Error(), "has not declared dependency") {
		t.Fatalf("undeclared Get with a Ready provider = %v, %v", value, err)
	}
	unchanged()
	must(t, p.Restart(deadline(t)))
	must(t, p.WaitReady(deadline(t)))
	next, found, err := gordis.Lookup(s, valueKey)
	if err != nil || !found || next == nil || next == old || *old != 1 || *next != 2 || read() != "cache:2" {
		t.Fatalf("replacement: old=%v next=%v found=%v err=%v", old, next, found, err)
	}
	unchanged()
	for _, snapshot := range h.Snapshot() {
		if snapshot.Leases != 0 {
			t.Fatalf("Lookup acquired a lease: %+v", snapshot)
		}
	}
	must(t, p.Unmount(deadline(t)))
	assertLookup(t, s, valueKey, (*int)(nil), false)
	if read() != "database" || *old != 1 || *next != 2 {
		t.Fatal("fallback or retained references changed after unload")
	}
	unchanged()
}

func TestLookupActivationPublicationAndFreeze(t *testing.T) {
	scopes := make(chan *gordis.Scope, 2)
	staged, publish := make(chan *gordis.Scope, 1), make(chan struct{})
	stopping, finishStop := make(chan struct{}), make(chan struct{})
	var publishOnce, stopOnce sync.Once
	databaseKey := gordis.NewKey[*int]("lookup.database")
	databaseValue, cacheValue := 42, 7
	database := definition("database", func(_ context.Context, s *gordis.Scope) error {
		return gordis.Provide(s, databaseKey, &databaseValue)
	})
	database.Provides = []gordis.ServiceSpec{databaseKey.Spec()}
	provider := definition("cache", func(_ context.Context, s *gordis.Scope) error {
		// Activate can discover other Ready services without Requires.
		value, found, err := gordis.Lookup(s, databaseKey)
		if err != nil || !found || value != &databaseValue {
			return fmt.Errorf("lookup during Activate: %v, %v, %v", value, found, err)
		}
		if err := gordis.Provide(s, valueKey, &cacheValue); err != nil {
			return err
		}
		value, found, err = gordis.Lookup(s, valueKey)
		if err != nil || found || value != nil {
			return fmt.Errorf("unpublished self lookup: %v, %v, %v", value, found, err)
		}
		if err := s.OnStop("gate", func(context.Context) error {
			close(stopping)
			<-finishStop
			return nil
		}); err != nil {
			return err
		}
		staged <- s
		<-publish
		return nil
	})
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	h := lookupHost(t, lookupCaller(scopes), database, provider)
	// Release gates before the Host cleanup even when an assertion fails.
	t.Cleanup(func() { publishOnce.Do(func() { close(publish) }); stopOnce.Do(func() { close(finishStop) }) })
	_, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "database", Plugin: "database"})
	must(t, err)
	_, err = h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "caller", Plugin: "lookup-caller"})
	must(t, err)
	callerScope := recv(t, scopes)
	p, err := h.Env().Mount(deadline(t), gordis.InstanceSpec{ID: "cache", Plugin: "cache"})
	must(t, err)
	providerScope := recv(t, staged)
	if snapshot := find(h, "cache"); snapshot.Phase != gordis.PhaseActivating {
		t.Fatal(snapshot)
	}
	assertLookup(t, callerScope, valueKey, (*int)(nil), false)
	publishOnce.Do(func() { close(publish) })
	must(t, p.WaitReady(deadline(t)))
	assertLookup(t, callerScope, valueKey, &cacheValue, true)
	assertLookup(t, providerScope, valueKey, &cacheValue, true)
	done := make(chan error, 1)
	go func() { done <- p.Unmount(context.Background()) }()
	recv(t, stopping)
	// The published service record still exists while OnStop blocks, but is hidden.
	assertLookup(t, callerScope, valueKey, (*int)(nil), false)
	assertLookupError(t, providerScope, databaseKey, gordis.ErrClosed)
	stopOnce.Do(func() { close(finishStop) })
	must(t, recv(t, done))
	assertLookup(t, callerScope, valueKey, (*int)(nil), false)
	assertLookupError(t, providerScope, valueKey, gordis.ErrClosed)
}

func TestLookupPendingProviderBecomesVisibleAfterDependencyAppears(t *testing.T) {
	scopes := make(chan *gordis.Scope, 1)
	value := 42
	provider := definition("cache", func(_ context.Context, s *gordis.Scope) error {
		if _, err := gordis.Get(s, p2ValueKey); err != nil {
			return err
		}
		return gordis.Provide(s, valueKey, &value)
	})
	provider.Requires = []gordis.ServiceSpec{p2ValueKey.Spec()}
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	h := lookupHost(t, lookupCaller(scopes), provider, new(p2Provider))
	_, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "caller", Plugin: "lookup-caller"})
	must(t, err)
	s := recv(t, scopes)
	p, err := h.Env().Mount(deadline(t), gordis.InstanceSpec{ID: "cache", Plugin: "cache"})
	must(t, err)
	awaitState(t, func() bool { return find(h, "cache").Phase == gordis.PhasePending })
	assertLookup(t, s, valueKey, (*int)(nil), false)
	_, err = h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "database", Plugin: "p2-provider"})
	must(t, err)
	must(t, p.WaitReady(deadline(t)))
	assertLookup(t, s, valueKey, &value, true)
	if snapshot := find(h, "caller"); snapshot.Generation != s.Generation() || snapshot.Phase != gordis.PhaseReady || len(snapshot.Dependencies) != 0 {
		t.Fatalf("provider readiness changed optional caller: %+v", snapshot)
	}
}

func TestLookupViewIsolationUsesNameAndFixedLabel(t *testing.T) {
	scopes := make(chan *gordis.Scope, 4)
	provided := make(chan [2]*int, 2)
	otherKey := gordis.NewKey[*int]("lookup.other")
	provider := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		cache, other := new(int), new(int)
		if err := gordis.Provide(s, valueKey, cache); err != nil {
			return err
		}
		if err := gordis.Provide(s, otherKey, other); err != nil {
			return err
		}
		provided <- [2]*int{cache, other}
		return nil
	})
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec(), otherKey.Spec()}
	h := lookupHost(t, lookupCaller(scopes), provider)
	values := make(map[string][2]*int)
	for _, label := range []string{"left", "right"} {
		_, err := h.Env().Isolate(valueKey, label).Isolate(otherKey, label).MountReady(deadline(t), gordis.InstanceSpec{
			ID: label, Plugin: "provider",
		})
		must(t, err)
		values[label] = recv(t, provided)
	}
	queries := []struct {
		id           string
		env          gordis.Env
		cache, other *int
		found        bool
	}{
		{"left", h.Env().Isolate(valueKey, "left").Isolate(otherKey, "right"), values["left"][0], values["right"][1], true},
		{"right", h.Env().Isolate(valueKey, "right").Isolate(otherKey, "left"), values["right"][0], values["left"][1], true},
		{"default", h.Env(), nil, nil, false},
		{"missing", h.Env().Isolate(valueKey, "missing").Isolate(otherKey, "missing"), nil, nil, false},
	}
	for _, query := range queries {
		_, err := query.env.MountReady(deadline(t), gordis.InstanceSpec{ID: "caller-" + query.id, Plugin: "lookup-caller"})
		must(t, err)
		s := recv(t, scopes)
		assertLookup(t, s, valueKey, query.cache, query.found)
		assertLookup(t, s, otherKey, query.other, query.found)
		// Deriving a new public View cannot redirect this generation's lookup.
		_ = s.View().Isolate(valueKey, "right")
		assertLookup(t, s, valueKey, query.cache, query.found)
	}
}

func TestLookupInvalidKeysExactTypesAndCallerLifetime(t *testing.T) {
	scopes := make(chan *gordis.Scope, 3)
	h := lookupHost(t, lookupCaller(scopes), new(p2Provider))
	_, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "provider", Plugin: "p2-provider"})
	must(t, err)
	c, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "caller", Plugin: "lookup-caller"})
	must(t, err)
	s := recv(t, scopes)
	assertLookupError(t, nil, p2ValueKey, gordis.ErrClosed)
	assertLookupError(t, &gordis.Scope{}, p2ValueKey, gordis.ErrClosed)
	for _, key := range []gordis.Key[*int]{{}, gordis.NewKey[*int]("")} {
		assertLookupContextError(t, s, key, "invalid service key")
	}
	// The actual value implements p2Value and is assignable to any, but neither
	// the concrete type nor any is the exact declared contract type.
	assertLookupContextError(t, s, gordis.NewKey[any](p2ValueKey.Name()), "contract mismatch")
	assertLookupContextError(t, s, gordis.NewKey[p2ValueString](p2ValueKey.Name()), "contract mismatch")
	value, found, err := gordis.Lookup(s, p2ValueKey)
	if err != nil || !found || value == nil {
		t.Fatalf("exact interface contract rejected: %v, %v, %v", value, found, err)
	}
	must(t, c.Restart(deadline(t)))
	must(t, c.WaitReady(deadline(t)))
	current := recv(t, scopes)
	assertLookupError(t, s, p2ValueKey, gordis.ErrStale)
	assertLookup(t, current, p2ValueKey, value, true)
	must(t, c.Unmount(deadline(t)))
	assertLookupError(t, current, p2ValueKey, gordis.ErrClosed)
	_, err = h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "caller", Plugin: "lookup-caller"})
	must(t, err)
	remounted := recv(t, scopes)
	assertLookupError(t, current, p2ValueKey, gordis.ErrClosed)
	assertLookup(t, remounted, p2ValueKey, value, true)
	must(t, h.Shutdown(deadline(t)))
	assertLookupError(t, remounted, p2ValueKey, gordis.ErrClosed)
}

func TestLookupDoesNotDrainOptionalCall(t *testing.T) {
	scopes := make(chan *gordis.Scope, 1)
	key := gordis.NewKey[func() error]("lookup.blocking-call")
	entered, resume, disposed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	var closed atomic.Bool
	closedErr := errors.New("service closed during optional call")
	provider := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		if err := s.Defer("resource", func(context.Context) error { closed.Store(true); close(disposed); return nil }); err != nil {
			return err
		}
		return gordis.Provide(s, key, func() error {
			close(entered)
			<-resume
			if closed.Load() {
				return closedErr
			}
			return nil
		})
	})
	provider.Provides = []gordis.ServiceSpec{key.Spec()}
	h := lookupHost(t, lookupCaller(scopes), provider)
	t.Cleanup(func() { resumeOnce.Do(func() { close(resume) }) })
	_, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "caller", Plugin: "lookup-caller"})
	must(t, err)
	s := recv(t, scopes)
	p, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "provider", Plugin: "provider"})
	must(t, err)
	call, found, err := gordis.Lookup(s, key)
	if err != nil || !found || call == nil {
		t.Fatalf("Lookup = %v, %v, %v", call == nil, found, err)
	}
	done := make(chan error, 1)
	go func() { done <- call() }()
	recv(t, entered)
	if snapshot := find(h, "provider"); snapshot.Leases != 0 {
		t.Fatalf("optional call registered a lease: %+v", snapshot)
	}
	// No release function or optional-call completion is needed for unload.
	must(t, p.Unmount(deadline(t)))
	recv(t, disposed)
	value, found, err := gordis.Lookup(s, key)
	if value != nil || found || err != nil {
		t.Fatalf("unloaded provider visible: %v, %v, %v", value == nil, found, err)
	}
	select {
	case err := <-done:
		t.Fatalf("optional call ended before its gate was released: %v", err)
	default:
	}
	resumeOnce.Do(func() { close(resume) })
	if err := recv(t, done); !errors.Is(err, closedErr) {
		t.Fatalf("old reference call = %v", err)
	}
}

func TestLookupConcurrentStopAndReplacement(t *testing.T) {
	scopes := make(chan *gordis.Scope, 1)
	stopping, proceed := make(chan uint64, 1), make(chan struct{})
	provider := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		value := int(s.Generation())
		if err := s.OnStop("gate", func(context.Context) error { stopping <- s.Generation(); <-proceed; return nil }); err != nil {
			return err
		}
		return gordis.Provide(s, valueKey, &value)
	})
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	h := lookupHost(t, lookupCaller(scopes), provider)
	t.Cleanup(func() { close(proceed) })
	_, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "caller", Plugin: "lookup-caller"})
	must(t, err)
	s := recv(t, scopes)
	p, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "provider", Plugin: "provider"})
	must(t, err)
	initial := find(h, "caller")
	stopReaders, cancel := context.WithCancel(context.Background())
	results, started := make(chan error, 4), make(chan struct{}, 4)
	for range 4 {
		go func() {
			// Synchronize after a real query, so replacement starts with active readers.
			value, found, err := gordis.Lookup(s, valueKey)
			if err != nil || !found || value == nil || *value != 1 {
				results <- fmt.Errorf("initial concurrent Lookup = %v, %v, %v", value, found, err)
				started <- struct{}{}
				return
			}
			started <- struct{}{}
			for stopReaders.Err() == nil {
				value, found, err := gordis.Lookup(s, valueKey)
				if err != nil || (found && (value == nil || *value < 1)) || (!found && value != nil) {
					results <- fmt.Errorf("concurrent Lookup = %v, %v, %v", value, found, err)
					return
				}
				runtime.Gosched()
			}
			results <- nil
		}()
	}
	t.Cleanup(func() {
		cancel()
		for range 4 {
			must(t, recv(t, results))
		}
	})
	for range 4 {
		recv(t, started)
	}
	for generation := uint64(1); generation <= 8; generation++ {
		old, found, err := gordis.Lookup(s, valueKey)
		if err != nil || !found || old == nil || *old != int(generation) {
			t.Fatalf("before replacement: %v, %v, %v", old, found, err)
		}
		done := make(chan error, 1)
		go func() { done <- p.Restart(context.Background()) }()
		if got := recv(t, stopping); got != generation {
			t.Fatalf("stopping generation = %d", got)
		}
		assertLookup(t, s, valueKey, (*int)(nil), false)
		proceed <- struct{}{}
		must(t, recv(t, done))
		must(t, p.WaitReady(deadline(t)))
		next, found, err := gordis.Lookup(s, valueKey)
		if err != nil || !found || next == nil || next == old || *next != int(generation+1) || *old != int(generation) {
			t.Fatalf("after replacement: old=%v next=%v found=%v err=%v", old, next, found, err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- p.Unmount(context.Background()) }()
	recv(t, stopping)
	assertLookup(t, s, valueKey, (*int)(nil), false)
	proceed <- struct{}{}
	must(t, recv(t, done))
	assertLookup(t, s, valueKey, (*int)(nil), false)
	if snapshot := find(h, "caller"); snapshot.Generation != initial.Generation || snapshot.Phase != gordis.PhaseReady || len(snapshot.Dependencies) != 0 || len(snapshot.Bindings) != 0 {
		t.Fatalf("concurrent discovery changed caller: %+v", snapshot)
	}
}

func TestLookupDetachedScopeDoesNotUseFixedBindings(t *testing.T) {
	value := 42
	run := lifecycle.New("detached", 17, []gordis.ServiceSpec{valueKey.Spec()}, nil, map[string]any{valueKey.Name(): &value}, nil)
	s := &gordis.Scope{Scope: run.Scope()}
	t.Cleanup(func() { run.Freeze(); run.Quiesce(); must(t, run.Dispose()) })
	check := func() {
		t.Helper()
		assertLookupError(t, s, valueKey, gordis.ErrLookupUnsupported)
		assertLookupError(t, s, gordis.NewKey[*int]("missing"), gordis.ErrLookupUnsupported)
		bound, err := gordis.Get(s, valueKey)
		if err != nil || bound != &value {
			t.Fatalf("fixed Get = %v, %v", bound, err)
		}
		if snapshot := run.Snapshot(); snapshot.Leases != 0 {
			t.Fatal(snapshot)
		}
	}
	check()
	err, _ := run.Commit(context.Background(), nil, nil, func(map[string]any) {})
	must(t, err)
	check()
	run.Freeze()
	assertLookupError(t, s, valueKey, gordis.ErrClosed)
}
