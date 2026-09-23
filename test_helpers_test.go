package gordis_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
)

var valueKey = gordis.NewKey[*int]("test.value")

type testPlugin struct {
	ID                 string
	Requires, Provides []gordis.ServiceSpec
	StartFunc          func(context.Context, *gordis.Scope) error
	NewFunc            func() gordis.Plugin
	ValidateFunc       func() error
}

func (p *testPlugin) Spec() gordis.PluginSpec {
	template := *p
	spec := gordis.PluginSpec{ID: p.ID, Requires: p.Requires, Provides: p.Provides}
	if p.NewFunc != nil {
		spec.New = p.NewFunc
	} else {
		spec.New = func() gordis.Plugin {
			copy := template
			return &copy
		}
	}
	return spec
}

func (p *testPlugin) Validate() error {
	if p.ValidateFunc != nil {
		return p.ValidateFunc()
	}
	return nil
}

func (p *testPlugin) Start(ctx context.Context, scope *gordis.Scope) error {
	if p.StartFunc == nil {
		return nil
	}
	return p.StartFunc(ctx, scope)
}

func definition(id string, fn func(context.Context, *gordis.Scope) error) *testPlugin {
	return &testPlugin{ID: id, StartFunc: fn}
}

func host(t *testing.T, plugins ...gordis.Plugin) *gordis.Host {
	t.Helper()
	specs := []gordis.InstanceSpec{}
	for _, plugin := range plugins {
		id := plugin.Spec().ID
		specs = append(specs, gordis.InstanceSpec{ID: id, Plugin: id})
	}
	h, err := gordis.NewHost(plugins, specs)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-deadline(t).Done():
		t.Fatal("timed out waiting for synchronization")
		var zero T
		return zero
	}
}

func find(h *gordis.Host, id string) gordis.Snapshot {
	for _, s := range h.Snapshot() {
		if s.ID == id {
			return s
		}
	}
	panic(id)
}

// Observe an explicit state transition, with a deadline but no timing sleeps.
func awaitState(t *testing.T, predicate func() bool) {
	t.Helper()
	ctx := deadline(t)
	for !predicate() {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for state")
		default:
			runtime.Gosched()
		}
	}
}
