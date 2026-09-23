package gordis_test

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/RussellLuo/gordis"
)

func stopEventually(t *testing.T, h *gordis.Host) {
	t.Helper()
	ctx := deadline(t)
	for {
		err := h.Stop(ctx)
		if !errors.Is(err, gordis.ErrBusy) {
			must(t, err)
			return
		}
		runtime.Gosched()
	}
}

func TestManagedFailureStopsOnlyDependentClosure(t *testing.T) {
	fail := make(chan struct{})
	cleaned := make(chan struct{})
	consumerCleaned := make(chan struct{})
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, s.Defer("resource", func(context.Context) error { close(cleaned); return nil }))
		must(t, gordis.Provide(s, valueKey, &v))
		return s.Go("worker", func(context.Context) error { <-fail; panic("broken") })
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		return s.Defer("registration", func(context.Context) error { close(consumerCleaned); return nil })
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	u := definition("unrelated", func(context.Context, *gordis.Scope) error { return nil })
	h := host(t, p, c, u)
	must(t, h.Start(deadline(t)))
	close(fail)
	recv(t, consumerCleaned)
	recv(t, cleaned)
	// Joining the existing stop is safe even while its last callback returns.
	must(t, h.StopInstance(deadline(t), "provider"))
	if find(h, "provider").State != gordis.Failed ||
		find(h, "consumer").State != gordis.Stopped ||
		find(h, "unrelated").State != gordis.Ready {
		t.Fatal(h.Snapshot())
	}
	if s := find(h, "consumer"); s.LastError != "" ||
		s.FailurePhase != "" ||
		!strings.Contains(s.StopReason, "provider generation 1") {
		t.Fatal(s)
	}
	must(t, h.Stop(deadline(t)))
}

func TestFailureClosureCrossesOwnershipAndServiceEdges(t *testing.T) {
	otherKey := gordis.NewKey[int]("child-service")
	fail := make(chan struct{})
	var cleaned []string
	node := func(id string) *testPlugin {
		return definition(id, func(_ context.Context, s *gordis.Scope) error {
			return s.Defer(id, func(context.Context) error { cleaned = append(cleaned, id); return nil })
		})
	}
	p := definition("source", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, s.Defer("source", func(context.Context) error { cleaned = append(cleaned, "source"); return nil }))
		must(t, gordis.Provide(s, valueKey, &v))
		return s.Go("worker", func(context.Context) error { <-fail; return errors.New("source broke") })
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	b := node("consumer-parent")
	b.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("owned-provider", func(_ context.Context, s *gordis.Scope) error {
		cleanup := func(context.Context) error {
			cleaned = append(cleaned, "owned-provider")
			return nil
		}
		must(t, s.Defer("owned-provider", cleanup))
		return gordis.Provide(s, otherKey, 1)
	})
	c.Provides = []gordis.ServiceSpec{otherKey.Spec()}
	d := node("external-consumer")
	d.Requires = []gordis.ServiceSpec{otherKey.Spec()}
	root, leaf := node("root"), node("leaf")
	h, err := gordis.NewHost([]gordis.Plugin{p, b, c, d, root, leaf}, []gordis.InstanceSpec{
		{ID: "source", Plugin: "source"}, {ID: "root", Plugin: "root"},
		{ID: "consumer-parent", Plugin: "consumer-parent", Parent: "root"},
		{ID: "owned-provider", Plugin: "owned-provider", Parent: "consumer-parent"},
		{ID: "external-consumer", Plugin: "external-consumer"},
		{ID: "leaf", Plugin: "leaf", Parent: "external-consumer"},
	})
	must(t, err)
	must(t, h.Start(deadline(t)))
	close(fail)
	awaitState(t, func() bool { return find(h, "source").CleanupComplete })
	must(t, h.StopInstance(deadline(t), "source"))
	if !reflect.DeepEqual(cleaned, []string{"leaf", "external-consumer", "owned-provider", "consumer-parent", "source"}) {
		t.Fatal(cleaned)
	}
	for _, id := range []string{"consumer-parent", "owned-provider", "external-consumer", "leaf"} {
		if s := find(h, id); s.State != gordis.Stopped ||
			!strings.Contains(s.StopReason, "source generation 1") ||
			s.LastError != "" {
			t.Fatal(s)
		}
	}
	if s := find(h, "root"); s.State != gordis.Ready || s.GroupReady {
		t.Fatal(s)
	}
	must(t, h.Stop(deadline(t)))
}

func TestFailureFreezesClosureDuringUnrelatedOperation(t *testing.T) {
	for _, operation := range []string{"start", "stop"} {
		t.Run(operation, func(t *testing.T) {
			fail := make(chan struct{})
			entered, unblock := make(chan struct{}), make(chan struct{})
			var scope *gordis.Scope
			p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
				v := 1
				must(t, gordis.Provide(s, valueKey, &v))
				return s.Go("worker", func(context.Context) error { <-fail; return errors.New("provider broke") })
			})
			p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
			c := definition("consumer", func(_ context.Context, s *gordis.Scope) error { scope = s; return nil })
			c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
			u := definition("unrelated", func(_ context.Context, s *gordis.Scope) error {
				block := func(context.Context) error { close(entered); <-unblock; return nil }
				if operation == "stop" {
					return s.Defer("blocked cleanup", block)
				}
				return block(context.Background())
			})
			h := host(t, p, c, u)
			must(t, h.StartInstance(deadline(t), "provider"))
			must(t, h.StartInstance(deadline(t), "consumer"))
			if operation == "stop" {
				must(t, h.StartInstance(deadline(t), "unrelated"))
			}
			ctx := deadline(t)
			done := make(chan error, 1)
			go func() {
				if operation == "stop" {
					done <- h.StopInstance(ctx, "unrelated")
				} else {
					done <- h.StartInstance(ctx, "unrelated")
				}
			}()
			recv(t, entered)
			close(fail)
			awaitState(t, func() bool { return find(h, "provider").State == gordis.Failed })
			if s := find(h, "consumer"); s.State != gordis.Stopping || s.CleanupPhase != "frozen" {
				t.Error(s)
			}
			if release, err := scope.Acquire(); err == nil {
				release()
				t.Error("failed dependency still accepts new consumer requests")
			}
			if err := scope.Go("late", func(context.Context) error { return nil }); !errors.Is(err, gordis.ErrClosed) {
				t.Error(err)
			}
			close(unblock)
			must(t, recv(t, done))
			stopEventually(t, h)
			if s := find(h, "consumer"); s.State != gordis.Stopped ||
				s.LastError != "" ||
				!strings.Contains(s.StopReason, "provider generation 1") {
				t.Fatal(s)
			}
		})
	}
}

func TestConsumerStartupRejectsFailedProvider(t *testing.T) {
	fail, entered, unblock := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cancelled := make(chan struct{})
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, gordis.Provide(s, valueKey, &v))
		return s.Go("worker", func(context.Context) error { <-fail; return errors.New("provider broke") })
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(ctx context.Context, _ *gordis.Scope) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-unblock // Even a callback ignoring cancellation must not publish readiness.
		return nil
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, p, c)
	must(t, h.StartInstance(deadline(t), "provider"))
	ctx := deadline(t)
	done := make(chan error, 1)
	go func() { done <- h.StartInstance(ctx, "consumer") }()
	recv(t, entered)
	close(fail)
	recv(t, cancelled)
	if s := find(h, "consumer"); s.State != gordis.Stopping || s.CleanupComplete {
		t.Error(s)
	}
	close(unblock)
	if err := recv(t, done); err == nil {
		t.Error("consumer reported ready after provider failure")
	}
	stopEventually(t, h)
	if s := find(h, "consumer"); s.State != gordis.Stopped || s.LastError != "" || s.StopReason == "" {
		t.Fatal(s)
	}
}

func TestFailureCascadePreservesIndependentErrors(t *testing.T) {
	providerFail, consumerFail := make(chan struct{}), make(chan struct{})
	entered, unblock := make(chan struct{}), make(chan struct{})
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, gordis.Provide(s, valueKey, &v))
		return s.Go("worker", func(context.Context) error { <-providerFail; return errors.New("provider original") })
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		return s.Go("worker", func(context.Context) error { <-consumerFail; return errors.New("consumer original") })
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	u := definition("unrelated", func(context.Context, *gordis.Scope) error { close(entered); <-unblock; return nil })
	h := host(t, p, c, u)
	must(t, h.StartInstance(deadline(t), "provider"))
	must(t, h.StartInstance(deadline(t), "consumer"))
	ctx := deadline(t)
	done := make(chan error, 1)
	go func() { done <- h.StartInstance(ctx, "unrelated") }()
	recv(t, entered)
	close(consumerFail)
	awaitState(t, func() bool { return find(h, "consumer").State == gordis.Failed })
	close(providerFail)
	awaitState(t, func() bool { return find(h, "provider").State == gordis.Failed })
	close(unblock)
	must(t, recv(t, done))
	stopEventually(t, h)
	for _, id := range []string{"provider", "consumer"} {
		if s := find(h, id); s.State != gordis.Failed ||
			!strings.Contains(s.LastError, id+" original") ||
			s.FailurePhase != "runtime" {
			t.Fatal(s)
		}
	}
}
