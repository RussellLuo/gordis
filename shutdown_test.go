package gordis_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
)

func TestStopTimeoutPinsProvidersAndJoinsCleanup(t *testing.T) {
	releaseTask := make(chan struct{})
	cancelled := make(chan struct{})
	quiesced := make(chan struct{})
	var providerClosed atomic.Bool
	var scopes []*gordis.Scope
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, s.Defer("provider", func(context.Context) error { providerClosed.Store(true); return nil }))
		return gordis.Provide(s, valueKey, &v)
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		scopes = append(scopes, s)
		must(t, s.OnStop("entrance", func(context.Context) error { close(quiesced); return nil }))
		return s.Go("stubborn", func(ctx context.Context) error { <-ctx.Done(); close(cancelled); <-releaseTask; return nil })
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, p, c)
	must(t, h.Start(deadline(t)))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- h.Stop(ctx) }()
	recv(t, quiesced)
	recv(t, cancelled)
	cancel()
	if err := recv(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if providerClosed.Load() {
		t.Fatal("provider released before consumer")
	}
	if find(h, "consumer").CleanupComplete {
		t.Fatal("stuck task reported complete")
	}
	if _, err := scopes[0].Acquire(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if err := h.StartInstance(deadline(t), "consumer"); !errors.Is(err, gordis.ErrBusy) {
		t.Fatal(err)
	}
	close(releaseTask)
	must(t, h.Stop(deadline(t)))
	if !providerClosed.Load() {
		t.Fatal("provider was not released")
	}
}

func TestRuntimeContextSurvivesStartupAndRequestLeaseDrains(t *testing.T) {
	var scope *gordis.Scope
	quiesced := make(chan struct{})
	closed := make(chan struct{})
	d := definition("plugin", func(_ context.Context, s *gordis.Scope) error {
		scope = s
		must(t, s.OnStop("entrance", func(context.Context) error { close(quiesced); return nil }))
		return s.Defer("resource", func(context.Context) error { close(closed); return nil })
	})
	h := host(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	must(t, h.Start(ctx))
	cancel()
	if scope.Context().Err() != nil {
		t.Fatal("startup cancellation killed runtime")
	}
	release, err := scope.Acquire()
	must(t, err)
	result := make(chan error, 1)
	go func() { result <- h.Stop(deadline(t)) }()
	recv(t, quiesced)
	select {
	case <-closed:
		t.Fatal("closed leased resource")
	default:
	}
	release()
	release()
	must(t, recv(t, result))
	recv(t, closed)
}

func TestCleanupErrorsContinueAndRetainDiagnostics(t *testing.T) {
	var order []int
	d := definition("plugin", func(_ context.Context, s *gordis.Scope) error {
		must(t, s.Defer("first", func(context.Context) error { order = append(order, 1); return nil }))
		return s.Defer("second", func(context.Context) error { order = append(order, 2); panic("cleanup failed") })
	})
	h := host(t, d)
	must(t, h.Start(deadline(t)))
	if err := h.Stop(deadline(t)); err == nil {
		t.Fatal("missing cleanup error")
	}
	if !reflect.DeepEqual(order, []int{2, 1}) {
		t.Fatal(order)
	}
	s := find(h, "plugin")
	if !s.CleanupComplete || s.State != gordis.Failed || !reflect.DeepEqual(s.Residuals, []string{"second"}) {
		t.Fatal(s)
	}
	if err := h.StartInstance(deadline(t), "plugin"); err == nil {
		t.Fatal("restarted over uncertain resources")
	}
}

func TestFailedConsumerCleanupPinsUpstream(t *testing.T) {
	var providerClosed atomic.Bool
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, s.Defer("provider", func(context.Context) error { providerClosed.Store(true); return nil }))
		return gordis.Provide(s, valueKey, &v)
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		return s.Defer("consumer", func(context.Context) error { return errors.New("still owns dependency") })
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, p, c)
	must(t, h.Start(deadline(t)))
	if err := h.Stop(deadline(t)); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if providerClosed.Load() {
		t.Fatal("upstream was released after uncertain consumer cleanup")
	}
	if s := find(h, "provider"); s.CleanupComplete || !reflect.DeepEqual(s.BlockedBy, []string{"consumer"}) {
		t.Fatal(s)
	}
	if err := h.StopInstance(deadline(t), "provider"); err == nil {
		t.Fatal("unloaded a pinned provider")
	}
}

func TestStopFreezesWholeClosureBeforeCallbacks(t *testing.T) {
	entered, unblock := make(chan struct{}), make(chan struct{})
	var scope *gordis.Scope
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		scope = s
		v := 1
		return gordis.Provide(s, valueKey, &v)
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		return s.OnStop("slow entrance", func(context.Context) error { close(entered); <-unblock; return nil })
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, p, c)
	must(t, h.Start(deadline(t)))
	ctx := deadline(t)
	done := make(chan error, 1)
	go func() { done <- h.Stop(ctx) }()
	recv(t, entered)
	if s := find(h, "provider"); s.State != gordis.Stopping || s.CleanupPhase != "frozen" {
		t.Error(s)
	}
	if release, err := scope.Acquire(); err == nil {
		release()
		t.Error("provider still accepts requests during consumer OnStop")
	}
	if err := scope.Defer("late", func(context.Context) error { return nil }); !errors.Is(err, gordis.ErrClosed) {
		t.Error(err)
	}
	close(unblock)
	must(t, recv(t, done))
}

func TestLateManagedCreationCleanupPinsProvider(t *testing.T) {
	created, finishCreate := make(chan struct{}), make(chan struct{})
	quiesced, disposing, finishDispose := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var providerClosed atomic.Bool
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, s.Defer("provider", func(context.Context) error { providerClosed.Store(true); return nil }))
		return gordis.Provide(s, valueKey, &v)
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		must(t, s.OnStop("entrance", func(context.Context) error { close(quiesced); return nil }))
		return s.Go("creator", func(context.Context) error {
			close(created)
			<-finishCreate
			if err := s.Defer("late", func(context.Context) error { return nil }); !errors.Is(err, gordis.ErrClosed) {
				return errors.New("late registration not rejected")
			}
			close(disposing)
			<-finishDispose
			return nil
		})
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, p, c)
	must(t, h.Start(deadline(t)))
	recv(t, created)
	ctx := deadline(t)
	done := make(chan error, 1)
	go func() { done <- h.Stop(ctx) }()
	recv(t, quiesced)
	close(finishCreate)
	recv(t, disposing)
	if providerClosed.Load() || find(h, "consumer").CleanupComplete {
		t.Error("resource released before rejected creation was reclaimed")
	}
	close(finishDispose)
	must(t, recv(t, done))
	if !providerClosed.Load() {
		t.Fatal("provider not released")
	}
}
