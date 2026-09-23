package gordis_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis"
)

type testStore struct {
	name       string
	generation uint64
	closed     bool
}

var storeKey = gordis.NewKey[*testStore]("test.store")

func storePlugin() gordis.Plugin {
	d := definition("store", func(_ context.Context, s *gordis.Scope) error {
		store := &testStore{name: s.ID(), generation: s.Generation()}
		if err := s.Defer("store", func(context.Context) error { store.closed = true; return nil }); err != nil {
			return err
		}
		return gordis.Provide(s, storeKey, store)
	})
	d.Provides = []gordis.ServiceSpec{storeKey.Spec()}
	return d
}

func TestInstanceSlotBindingsAndGenerations(t *testing.T) {
	stores := map[string]*testStore{}
	scopes := map[string]*gordis.Scope{}
	c := definition("collector", func(_ context.Context, s *gordis.Scope) error {
		store, err := gordis.Get(s, storeKey)
		if err != nil {
			return err
		}
		stores[s.ID()], scopes[s.ID()] = store, s
		return s.Defer("flush", func(context.Context) error {
			if store.closed {
				return fmt.Errorf("store %s closed before its consumer", store.name)
			}
			return nil
		})
	})
	c.Requires = []gordis.ServiceSpec{storeKey.Spec()}
	inputs := map[string]string{storeKey.Name(): "primary"}
	outputs := map[string]string{storeKey.Name(): "primary"}
	h, err := gordis.NewHost([]gordis.Plugin{storePlugin(), c}, []gordis.InstanceSpec{
		{ID: "store-main", Plugin: "store", Outputs: outputs},
		{ID: "store-audit", Plugin: "store", Outputs: map[string]string{storeKey.Name(): "audit"}},
		{ID: "collector-main", Plugin: "collector", Inputs: inputs},
		{ID: "collector-audit", Plugin: "collector", Inputs: map[string]string{storeKey.Name(): "audit"}},
	})
	must(t, err)
	inputs[storeKey.Name()], outputs[storeKey.Name()] = "mutated", "mutated"
	must(t, h.Start(deadline(t)))
	main, audit := stores["collector-main"], stores["collector-audit"]
	if main == audit || main.name != "store-main" || audit.name != "store-audit" {
		t.Fatal(main, audit)
	}
	want := []gordis.BindingSnapshot{{Service: storeKey.Name(), Slot: "primary", Provider: "store-main", Generation: 1}}
	if s := find(h, "collector-main"); !reflect.DeepEqual(s.Bindings, want) {
		t.Fatal(s)
	}
	// Returned diagnostic maps/slices must not mutate the graph or live binding.
	find(h, "collector-main").Bindings[0].Slot = "mutated snapshot"
	find(h, "store-main").ProvidedSlots[storeKey.Name()] = "mutated snapshot"
	oldScope := scopes["collector-main"]
	must(t, h.StopInstance(deadline(t), "collector-main"))
	must(t, h.StopInstance(deadline(t), "store-main"))
	must(t, h.StartInstance(deadline(t), "store-main"))
	must(t, h.StartInstance(deadline(t), "collector-main"))
	if stores["collector-main"] == main || !main.closed || stores["collector-main"].generation != 2 {
		t.Fatal("main binding did not move to a new activation")
	}
	if stores["collector-audit"] != audit || audit.closed || find(h, "collector-audit").Bindings[0].Generation != 1 {
		t.Fatal("restarting main changed audit's fixed binding")
	}
	if s := find(h, "collector-main"); s.Bindings[0].Generation != 2 || s.Bindings[0].Slot != "primary" {
		t.Fatal(s)
	}
	if _, err := gordis.Get(oldScope, storeKey); err == nil {
		t.Fatal("old consumer scope returned the new provider")
	}
	must(t, h.Stop(deadline(t)))
}

type testRegistry struct {
	database *testStore
	entries  map[string]string
}

func (r *testRegistry) register(owner *gordis.Scope) error {
	r.entries[owner.ID()] = r.database.name
	return owner.Defer("registration", func(context.Context) error {
		if r.database.closed {
			return fmt.Errorf("registry database was closed before registration cleanup")
		}
		delete(r.entries, owner.ID())
		return nil
	})
}

func TestRegistryKeepsOwnDependenciesAndCallerOwnership(t *testing.T) {
	registryKey := gordis.NewKey[*testRegistry]("test.registry")
	var registry *testRegistry
	r := definition("registry", func(_ context.Context, s *gordis.Scope) error {
		db, err := gordis.Get(s, storeKey)
		if err != nil {
			return err
		}
		registry = &testRegistry{database: db, entries: map[string]string{}}
		return gordis.Provide(s, registryKey, registry)
	})
	r.Requires = []gordis.ServiceSpec{storeKey.Spec()}
	r.Provides = []gordis.ServiceSpec{registryKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		db, err := gordis.Get(s, storeKey)
		if err != nil {
			return err
		}
		if db.name != "db-b" {
			return fmt.Errorf("consumer got database %s", db.name)
		}
		registry, err := gordis.Get(s, registryKey)
		if err != nil {
			return err
		}
		return registry.register(s)
	})
	c.Requires = []gordis.ServiceSpec{storeKey.Spec(), registryKey.Spec()}
	h, err := gordis.NewHost([]gordis.Plugin{storePlugin(), r, c}, []gordis.InstanceSpec{
		{ID: "db-a", Plugin: "store", Outputs: map[string]string{storeKey.Name(): "db.a"}},
		{ID: "db-b", Plugin: "store", Outputs: map[string]string{storeKey.Name(): "db.b"}},
		{ID: "registry", Plugin: "registry", Inputs: map[string]string{storeKey.Name(): "db.a"}},
		{ID: "consumer", Plugin: "consumer", Inputs: map[string]string{storeKey.Name(): "db.b"}},
	})
	must(t, err)
	must(t, h.Start(deadline(t)))
	if registry.entries["consumer"] != "db-a" {
		t.Fatal("caller's database replaced the registry's own dependency")
	}
	must(t, h.StopInstance(deadline(t), "consumer"))
	if len(registry.entries) != 0 || registry.database.closed || find(h, "registry").State != gordis.Ready {
		t.Fatal("registration lifetime did not follow its caller")
	}
	must(t, h.Stop(deadline(t)))
}
