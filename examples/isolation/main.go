// This example connects two consumers to different providers of the same
// typed service through isolated Envs.
//
// Run from the repository root: go run ./examples/isolation
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

func (*storePlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide(scope, storeKey, &store{id: scope.ID()})
}

type collectorPlugin struct{}

func (*collectorPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "collector",
		Requires: []gordis.ServiceSpec{storeKey.Spec()},
		New:      func() gordis.Plugin { return new(collectorPlugin) },
	}
}

func (*collectorPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	db, err := gordis.Get(scope, storeKey)
	if err != nil {
		return err
	}
	fmt.Printf("%s -> %s\n", scope.ID(), db.id)
	return nil
}

func run() error {
	host, err := gordis.NewHost([]gordis.Plugin{new(storePlugin), new(collectorPlugin)})
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	ctx := context.Background()
	mainEnv := host.Env().Isolate(storeKey, "store.main")
	auditEnv := host.Env().Isolate(storeKey, "store.audit")

	if _, err := mainEnv.MountReady(ctx, gordis.InstanceSpec{ID: "store-main", Plugin: "store"}); err != nil {
		return err
	}
	if _, err := auditEnv.MountReady(ctx, gordis.InstanceSpec{ID: "store-audit", Plugin: "store"}); err != nil {
		return err
	}

	if _, err := mainEnv.MountReady(ctx, gordis.InstanceSpec{ID: "collector-main", Plugin: "collector"}); err != nil {
		return err
	}
	if _, err := auditEnv.MountReady(ctx, gordis.InstanceSpec{ID: "collector-audit", Plugin: "collector"}); err != nil {
		return err
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
