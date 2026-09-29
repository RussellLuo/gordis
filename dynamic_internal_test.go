package gordis

import (
	"context"
	"errors"
	"testing"
	"time"
)

type internalPlanPlugin struct{}

func (*internalPlanPlugin) Spec() PluginSpec {
	return PluginSpec{
		ID:  "internal-plan",
		New: func() Plugin { return new(internalPlanPlugin) },
	}
}

func (*internalPlanPlugin) Activate(context.Context, *Scope) error { return nil }

func TestInternalChangePlanRejectsStaleHostRevision(t *testing.T) {
	h, err := NewHost(new(internalPlanPlugin))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	one, err := h.Env().MountReady(ctx, InstanceSpec{ID: "one", Plugin: "internal-plan"})
	if err != nil {
		t.Fatal(err)
	}
	changes := h.Changes()
	changes.Restart(one)
	plan, err := changes.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Env().MountReady(ctx, InstanceSpec{ID: "two", Plugin: "internal-plan"}); err != nil {
		t.Fatal(err)
	}
	if err := h.waitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, record, err := h.submit(ctx, plan, 0); record != nil || !errors.Is(err, ErrStale) {
		t.Fatalf("stale record=%v err=%v", record, err)
	}
}
