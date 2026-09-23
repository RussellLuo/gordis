package gordis_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis"
)

func TestOptionalStartupFailurePreservesParentGroup(t *testing.T) {
	for _, failure := range []string{"optional", "nested"} {
		t.Run(failure, func(t *testing.T) {
			startupErr := errors.New("optional integration unavailable")
			var cleaned []string
			d := definition("node", func(_ context.Context, s *gordis.Scope) error {
				if err := s.Defer("node", func(context.Context) error {
					cleaned = append(cleaned, s.ID())
					return nil
				}); err != nil {
					return err
				}
				if s.ID() == failure {
					return startupErr
				}
				return nil
			})
			h, err := gordis.NewHost([]gordis.Plugin{d}, []gordis.InstanceSpec{
				{ID: "parent", Plugin: "node"},
				{ID: "optional", Plugin: "node", Parent: "parent", Optional: true},
				{ID: "nested", Plugin: "node", Parent: "optional"},
				{ID: "required", Plugin: "node", Parent: "parent"},
			})
			must(t, err)
			if err := h.Start(deadline(t)); !errors.Is(err, startupErr) {
				t.Fatal("optional failure was not reported", err)
			}
			if s := find(h, "parent"); s.State != gordis.Ready || !s.GroupReady {
				t.Fatal(s)
			}
			if s := find(h, "required"); s.State != gordis.Ready {
				t.Fatal(s)
			}
			if s := find(h, failure); s.State != gordis.Failed || !s.CleanupComplete {
				t.Fatal(s)
			}
			want := []string{"optional"}
			if failure == "nested" {
				want = []string{"nested", "optional"}
			}
			if !reflect.DeepEqual(cleaned, want) {
				t.Fatal("unexpected cleanup before parent stops", cleaned)
			}
			must(t, h.Stop(deadline(t)))
		})
	}
}

func TestOptionalFailureAffectsRequiredConsumer(t *testing.T) {
	for _, phase := range []string{"startup", "runtime"} {
		t.Run(phase, func(t *testing.T) {
			fail := make(chan struct{})
			failure := errors.New("optional provider failed")
			var cleaned []string
			parent := definition("parent", func(context.Context, *gordis.Scope) error { return nil })
			provider := definition("optional", func(_ context.Context, s *gordis.Scope) error {
				v := 1
				must(t, s.Defer("provider", func(context.Context) error {
					v = 0
					cleaned = append(cleaned, "optional")
					return nil
				}))
				must(t, gordis.Provide(s, valueKey, &v))
				if phase == "startup" {
					return failure
				}
				return s.Go("worker", func(context.Context) error { <-fail; return failure })
			})
			provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
			consumer := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
				value, err := gordis.Get(s, valueKey)
				must(t, err)
				return s.Defer("consumer", func(context.Context) error {
					if *value != 1 {
						return errors.New("provider released before consumer")
					}
					cleaned = append(cleaned, "consumer")
					return nil
				})
			})
			consumer.Requires = []gordis.ServiceSpec{valueKey.Spec()}
			h, err := gordis.NewHost([]gordis.Plugin{parent, provider, consumer}, []gordis.InstanceSpec{
				{ID: "parent", Plugin: "parent"},
				{ID: "optional", Plugin: "optional", Parent: "parent", Optional: true},
				{ID: "consumer", Plugin: "consumer", Parent: "parent"},
			})
			must(t, err)
			err = h.Start(deadline(t))
			if phase == "startup" {
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			} else {
				must(t, err)
				if !find(h, "parent").GroupReady {
					t.Fatal(h.Snapshot())
				}
				close(fail)
				awaitState(t, func() bool { return find(h, "optional").CleanupComplete })
				must(t, h.StopInstance(deadline(t), "optional"))
			}
			if s := find(h, "parent"); s.State != gordis.Ready || s.GroupReady {
				t.Fatal("required consumer was incorrectly treated as optional", s)
			}
			if s := find(h, "optional"); s.State != gordis.Failed || !s.CleanupComplete {
				t.Fatal(s)
			}
			if s := find(h, "consumer"); s.State == gordis.Ready || s.StopReason == "" || s.LastError != "" {
				t.Fatal(s)
			}
			want := []string{"optional"}
			if phase == "runtime" {
				want = []string{"consumer", "optional"}
			}
			if !reflect.DeepEqual(cleaned, want) {
				t.Fatal(cleaned)
			}
			must(t, h.Stop(deadline(t)))
		})
	}
}
