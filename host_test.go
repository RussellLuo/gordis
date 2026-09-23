package gordis_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/RussellLuo/gordis"
)

func TestCallbackReentryAndConcurrentSnapshots(t *testing.T) {
	var h *gordis.Host
	d := definition("plugin", func(ctx context.Context, s *gordis.Scope) error {
		if len(h.Snapshot()) != 1 {
			return errors.New("snapshot failed")
		}
		if err := h.StartInstance(ctx, "plugin"); !errors.Is(err, gordis.ErrBusy) {
			return fmt.Errorf("reentry: %v", err)
		}
		return s.Go("worker", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	})
	h = host(t, d)
	must(t, h.Start(deadline(t)))
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				h.Snapshot()
			}
		}()
	}
	must(t, h.Stop(deadline(t)))
	wg.Wait()
}

func TestDependencyOrderAndResourceOwnership(t *testing.T) {
	var events []string
	provider := definition("z-provider", func(_ context.Context, s *gordis.Scope) error {
		events = append(events, "provider start")
		v := 42
		must(t, s.Defer("store", func(context.Context) error { events = append(events, "provider close"); return nil }))
		return gordis.Provide(s, valueKey, &v)
	})
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	consumer := definition("a-consumer", func(_ context.Context, s *gordis.Scope) error {
		v, err := gordis.Get(s, valueKey)
		if err != nil {
			return err
		}
		events = append(events, fmt.Sprintf("consumer %d", *v))
		must(t, s.Defer("first", func(context.Context) error { events = append(events, "first"); return nil }))
		return s.Defer("second", func(context.Context) error { events = append(events, "second"); return nil })
	})
	consumer.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, consumer, provider)
	must(t, h.Start(deadline(t)))
	if err := h.StopInstance(deadline(t), "z-provider"); err == nil || !strings.Contains(err.Error(), "a-consumer") {
		t.Fatal(err)
	}
	must(t, h.Stop(deadline(t)))
	must(t, h.Stop(deadline(t)))
	want := []string{"provider start", "consumer 42", "second", "first", "provider close"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("%v != %v", events, want)
	}
}
