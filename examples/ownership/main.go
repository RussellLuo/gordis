// This example mounts an owned child from a parent activation. Restarting the
// parent cleans up the old child before both are recreated for a new generation.
//
// Run from the repository root: go run ./examples/ownership
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/RussellLuo/gordis"
)

type ownerPlugin struct{}

func (*ownerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "owner", New: func() gordis.Plugin { return new(ownerPlugin) }}
}

func (*ownerPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	fmt.Printf("start %s / generation %d\n", scope.ID(), scope.Generation())
	if err := scope.Defer("owner", func(context.Context) error {
		fmt.Printf("close %s / generation %d\n", scope.ID(), scope.Generation())
		return nil
	}); err != nil {
		return err
	}
	_, err := scope.Env().Mount(ctx, gordis.InstanceSpec{ID: "child", Plugin: "child"})
	return err
}

type childPlugin struct{}

func (*childPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "child", New: func() gordis.Plugin { return new(childPlugin) }}
}

func (*childPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	fmt.Printf("start %s / generation %d\n", scope.ID(), scope.Generation())
	return scope.Defer("child", func(context.Context) error {
		fmt.Printf("close %s / generation %d\n", scope.ID(), scope.Generation())
		return nil
	})
}

func run() error {
	host, err := gordis.NewHost(new(ownerPlugin), new(childPlugin))
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	ctx := context.Background()
	owner, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "owner", Plugin: "owner"})
	if err != nil {
		return err
	}

	if err := owner.Restart(ctx); err != nil {
		return err
	}
	return owner.WaitReady(ctx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
