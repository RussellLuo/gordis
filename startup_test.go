package gordis_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
)

func TestFailedStartNeverPublishesAndRollsBack(t *testing.T) {
	var closed, consumer atomic.Int32
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		must(t, s.Defer("resource", func(context.Context) error { closed.Add(1); return nil }))
		must(t, gordis.Provide(s, valueKey, &v))
		return errors.New("not ready")
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(context.Context, *gordis.Scope) error { consumer.Add(1); return nil })
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, p, c)
	for generation := uint64(1); generation <= 2; generation++ {
		if err := h.Start(deadline(t)); err == nil {
			t.Fatal("expected error")
		}
		s := find(h, "provider")
		if s.State != gordis.Failed || !s.CleanupComplete || s.Generation != generation {
			t.Fatal(s)
		}
	}
	if consumer.Load() != 0 || closed.Load() != 2 {
		t.Fatal(consumer.Load(), closed.Load())
	}
}

func TestStartupTimeoutDoesNotLieAboutCleanup(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	cleaned := make(chan struct{})
	d := definition("slow", func(_ context.Context, s *gordis.Scope) error {
		must(t, s.Defer("resource", func(context.Context) error { close(cleaned); return nil }))
		close(entered)
		<-release
		return nil
	})
	h := host(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- h.Start(ctx) }()
	recv(t, entered)
	cancel()
	if err := recv(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s := find(h, "slow"); s.State != gordis.Starting || s.CleanupComplete {
		t.Fatal(s)
	}
	close(release)
	recv(t, cleaned)
}
