// This example connects two collectors to different store instances through
// service slots. An owned child also restarts with its parent, even though it
// has no service dependency on that parent.
//
// Run from the repository root: go run ./examples/composition
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/RussellLuo/gordis"
)

type store struct{ id string }

var storeKey = gordis.NewKey[*store]("example.store")

type storePlugin struct{}

func (*storePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "store",
		Provides: []gordis.ServiceSpec{storeKey.Spec()},
		New:      func() gordis.Plugin { return new(storePlugin) },
	}
}

func (*storePlugin) Start(_ context.Context, scope *gordis.Scope) error {
	resource := &store{id: scope.ID()}
	cleanup := func(context.Context) error {
		fmt.Println("close", resource.id)
		return nil
	}
	if err := scope.Defer("store", cleanup); err != nil {
		return err
	}
	return gordis.Provide(scope, storeKey, resource)
}

type collectorPlugin struct{}

func (*collectorPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "collector",
		Requires: []gordis.ServiceSpec{storeKey.Spec()},
		New:      func() gordis.Plugin { return new(collectorPlugin) },
	}
}

func (*collectorPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	db, err := gordis.Get(scope, storeKey)
	if err != nil {
		return err
	}
	fmt.Printf("%s / generation %d -> %s\n", scope.ID(), scope.Generation(), db.id)
	return scope.Defer("collector", func(context.Context) error {
		fmt.Println("close", scope.ID(), "using", db.id)
		return nil
	})
}

type childPlugin struct{}

func (*childPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "child", New: func() gordis.Plugin { return new(childPlugin) }}
}

func (*childPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	return scope.Defer("child", func(context.Context) error { fmt.Println("close", scope.ID()); return nil })
}

func run() error {
	h, err := gordis.NewHost(
		[]gordis.Plugin{new(storePlugin), new(collectorPlugin), new(childPlugin)},
		[]gordis.InstanceSpec{
			{ID: "store-main", Plugin: "store", Outputs: map[string]string{storeKey.Name(): "store.primary"}},
			{ID: "store-audit", Plugin: "store", Outputs: map[string]string{storeKey.Name(): "store.audit"}},
			{ID: "collector-main", Plugin: "collector", Inputs: map[string]string{storeKey.Name(): "store.primary"}},
			{ID: "collector-audit", Plugin: "collector", Inputs: map[string]string{storeKey.Name(): "store.audit"}},
			{ID: "child-main", Plugin: "child", Parent: "collector-main"},
		},
	)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := h.Start(ctx); err != nil {
		return err
	}
	if err := h.StopInstance(ctx, "collector-main"); err != nil {
		return err
	}
	if err := h.StartInstance(ctx, "collector-main"); err != nil {
		return err
	}
	for _, snapshot := range h.Snapshot() {
		if snapshot.Parent != "" {
			fmt.Printf(
				"%s / generation %d belongs to %s / generation %d\n",
				snapshot.ID, snapshot.Generation, snapshot.Parent, snapshot.ParentGeneration,
			)
		}
	}
	return h.Stop(ctx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
