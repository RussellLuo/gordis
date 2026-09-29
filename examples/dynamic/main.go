// This example changes one running Host with a ChangeSet. It removes the
// provider of a ready consumer, waits for the consumer to become pending, then
// restores the provider and brings both back with new generations.
//
// Run from the repository root: go run ./examples/dynamic
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/RussellLuo/gordis"
)

type Source interface{ Read() uint64 }
type source struct{ generation uint64 }

func (s source) Read() uint64 { return s.generation }

var sourceKey = gordis.NewKey[Source]("example.source/1")

type sourcePlugin struct{}

func (*sourcePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "source",
		Provides: []gordis.ServiceSpec{sourceKey.Spec()},
		New:      func() gordis.Plugin { return new(sourcePlugin) },
	}
}

func (*sourcePlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide[Source](scope, sourceKey, source{generation: scope.Generation()})
}

type consumerPlugin struct{}

func (*consumerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "consumer",
		Requires: []gordis.ServiceSpec{sourceKey.Spec()},
		New:      func() gordis.Plugin { return new(consumerPlugin) },
	}
}

func (*consumerPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	v, err := gordis.Get(scope, sourceKey)
	if err == nil {
		fmt.Printf("consumer generation %d reads source generation %d\n", scope.Generation(), v.Read())
	}
	return err
}

func run() error {
	h, err := gordis.NewHost(new(sourcePlugin), new(consumerPlugin))
	if err != nil {
		return err
	}
	defer h.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	source, err := h.Env().MountReady(ctx, gordis.InstanceSpec{ID: "source", Plugin: "source"})
	if err != nil {
		return err
	}
	if _, err = h.Env().MountReady(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "consumer"}); err != nil {
		return err
	}

	// Removing the provider intentionally leaves the consumer pending.
	changes := h.Changes()
	changes.Unmount(source)
	removed, err := changes.Apply(ctx)
	if err != nil {
		return err
	}
	if err = removed.Wait(ctx); err != nil {
		return err
	}
	fmt.Println("consumer pending: source removed")

	restored, err := removed.Restore(ctx)
	if err != nil {
		return err
	}
	return restored.WaitReady(ctx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
