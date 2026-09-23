package gordis

import (
	"context"
	"errors"
	"testing"
)

type internalNoopPlugin struct{}

func (*internalNoopPlugin) Spec() PluginSpec {
	return PluginSpec{ID: "d", New: func() Plugin { return new(internalNoopPlugin) }}
}

func (*internalNoopPlugin) Start(context.Context, *Scope) error { return nil }

func TestDeletedGenerationCannotFailReplacement(t *testing.T) {
	h, err := NewHost([]Plugin{new(internalNoopPlugin)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := InstanceSpec{ID: "same", Plugin: "d"}
	apply := func(c Change) {
		t.Helper()
		p, err := h.Preview(c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Apply(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	apply(Change{Upsert: []InstanceSpec{spec}})
	old := h.instances["same"].scope
	apply(Change{Remove: []string{"same"}})
	apply(Change{Upsert: []InstanceSpec{spec}})
	h.runtimeFailure(old.ID(), old.Generation(), errors.New("late old generation callback"))
	if s := h.Snapshot()[0]; s.State != Ready || s.Generation != 2 || s.LastError != "" {
		t.Fatal(s)
	}
	lateDisposer := func(context.Context) error {
		t.Error("old disposer accepted")
		return nil
	}
	if err := old.Defer("late disposer", lateDisposer); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
