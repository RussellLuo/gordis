package gordis_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
)

func TestOwnershipGenerationAndGroupReadiness(t *testing.T) {
	entered, unblock := make(chan struct{}), make(chan struct{})
	var events []string
	d := definition("node", func(_ context.Context, s *gordis.Scope) error {
		if s.ID() == "child" && s.Generation() == 1 {
			close(entered)
			<-unblock
		}
		return s.Defer("node", func(context.Context) error { events = append(events, s.ID()); return nil })
	})
	h, err := gordis.NewHost([]gordis.Plugin{d}, []gordis.InstanceSpec{
		{ID: "parent", Plugin: "node"},
		{ID: "child", Plugin: "node", Parent: "parent"},
		{ID: "grandchild", Plugin: "node", Parent: "child"},
	})
	must(t, err)
	if err := h.StartInstance(deadline(t), "child"); err == nil {
		t.Fatal("started a child without its parent")
	}
	ctx := deadline(t)
	done := make(chan error, 1)
	go func() { done <- h.StartInstance(ctx, "parent") }()
	recv(t, entered)
	if s := find(h, "parent"); s.State != gordis.Ready || s.GroupReady {
		t.Error(s)
	}
	if s := find(h, "child"); s.Parent != "parent" || s.ParentGeneration != 1 || len(s.Dependencies) != 0 {
		t.Error(s)
	}
	close(unblock)
	must(t, recv(t, done))
	if !find(h, "parent").GroupReady {
		t.Fatal(h.Snapshot())
	}
	must(t, h.StopInstance(deadline(t), "child"))
	if s := find(h, "parent"); s.State != gordis.Ready || s.GroupReady {
		t.Fatal(s)
	}
	must(t, h.StartInstance(deadline(t), "child"))
	if s := find(h, "child"); s.Generation != 2 || s.ParentGeneration != 1 {
		t.Fatal(s)
	}
	events = nil
	must(t, h.StopInstance(deadline(t), "parent"))
	if !reflect.DeepEqual(events, []string{"grandchild", "child", "parent"}) {
		t.Fatal(events)
	}
	must(t, h.StartInstance(deadline(t), "parent"))
	if s := find(h, "child"); s.Generation != 3 || s.ParentGeneration != 2 {
		t.Fatal(s)
	}
	if s := find(h, "grandchild"); s.Generation != 3 || s.ParentGeneration != 3 {
		t.Fatal(s)
	}
	must(t, h.Stop(deadline(t)))
}

func TestOwnedCleanupPinsParent(t *testing.T) {
	var parentClosed atomic.Bool
	parent := definition("parent", func(_ context.Context, s *gordis.Scope) error {
		return s.Defer("parent", func(context.Context) error { parentClosed.Store(true); return nil })
	})
	child := definition("child", func(_ context.Context, s *gordis.Scope) error {
		return s.Defer("child", func(context.Context) error { return errors.New("child resource still in use") })
	})
	h, err := gordis.NewHost([]gordis.Plugin{parent, child}, []gordis.InstanceSpec{
		{ID: "parent", Plugin: "parent"}, {ID: "child", Plugin: "child", Parent: "parent"},
	})
	must(t, err)
	must(t, h.Start(deadline(t)))
	if err := h.StopInstance(deadline(t), "parent"); err == nil {
		t.Fatal("expected cleanup error")
	}
	if parentClosed.Load() {
		t.Fatal("parent closed while child retained resources")
	}
	if s := find(h, "parent"); s.CleanupComplete || !reflect.DeepEqual(s.BlockedBy, []string{"child"}) {
		t.Fatal(s)
	}
	if err := h.StartInstance(deadline(t), "parent"); err == nil {
		t.Fatal("parent restarted over child's old generation")
	}
}

func TestStopOwnedSubtreeRejectsExternalConsumer(t *testing.T) {
	parent := definition("parent", func(context.Context, *gordis.Scope) error { return nil })
	child := definition("child", func(_ context.Context, s *gordis.Scope) error {
		v := 1
		return gordis.Provide(s, valueKey, &v)
	})
	child.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	consumer := definition("external", func(context.Context, *gordis.Scope) error { return nil })
	consumer.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	h, err := gordis.NewHost([]gordis.Plugin{parent, child, consumer}, []gordis.InstanceSpec{
		{ID: "parent", Plugin: "parent"},
		{ID: "child", Plugin: "child", Parent: "parent"},
		{ID: "external", Plugin: "external"},
	})
	must(t, err)
	must(t, h.Start(deadline(t)))
	if err := h.StopInstance(deadline(t), "parent"); err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatal(err)
	}
	for _, s := range h.Snapshot() {
		if s.State != gordis.Ready {
			t.Fatal("rejected stop changed state", s)
		}
	}
	must(t, h.StopInstance(deadline(t), "external"))
	must(t, h.StopInstance(deadline(t), "parent"))
}

func TestChildFailurePreservesParent(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(map[bool]string{false: "required", true: "optional"}[optional], func(t *testing.T) {
			fail := make(chan struct{})
			parent := definition("parent", func(context.Context, *gordis.Scope) error { return nil })
			child := definition("child", func(_ context.Context, s *gordis.Scope) error {
				return s.Go("worker", func(context.Context) error { <-fail; return errors.New("child broke") })
			})
			h, err := gordis.NewHost([]gordis.Plugin{parent, child}, []gordis.InstanceSpec{
				{ID: "parent", Plugin: "parent"}, {ID: "child", Plugin: "child", Parent: "parent", Optional: optional},
			})
			must(t, err)
			must(t, h.Start(deadline(t)))
			close(fail)
			awaitState(t, func() bool { return find(h, "child").CleanupComplete })
			must(t, h.StopInstance(deadline(t), "child"))
			if s := find(h, "parent"); s.State != gordis.Ready || s.GroupReady != optional || s.StopReason != "" {
				t.Fatal(s)
			}
			must(t, h.Stop(deadline(t)))
		})
	}
}
