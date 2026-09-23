// This example changes one running Host with Preview and Apply. A consumer
// waits for a missing provider, becomes ready when it appears, and waits again
// when it is removed. Restore brings both back with new generations.
//
// Run from the repository root: go run ./examples/dynamic
package main

import (
	"context"
	"errors"
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

func (*sourcePlugin) Start(_ context.Context, scope *gordis.Scope) error {
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

func (*consumerPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	v, err := gordis.Get(scope, sourceKey)
	if err == nil {
		fmt.Printf("consumer generation %d reads source generation %d\n", scope.Generation(), v.Read())
	}
	return err
}

func submit(ctx context.Context, h *gordis.Host, change gordis.Change) (uint64, error) {
	plan, err := h.Preview(change)
	if err != nil {
		return 0, err
	}
	return h.Apply(ctx, plan)
}

func expectConsumer(h *gordis.Host, want gordis.State) error {
	for _, s := range h.Snapshot() {
		if s.ID == "consumer" {
			fmt.Printf("consumer: %s (generation %d)\n", s.State, s.Generation)
			if s.State != want {
				return fmt.Errorf("consumer: got %s, want %s", s.State, want)
			}
			return nil
		}
	}
	return errors.New("consumer is missing")
}

func run() (err error) {
	// Initial graphs are strict, so start empty and add the waiting consumer later.
	h, err := gordis.NewHost([]gordis.Plugin{new(sourcePlugin), new(consumerPlugin)}, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.Stop(context.Background())) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = h.Start(ctx); err != nil {
		return err
	}
	consumer := gordis.InstanceSpec{ID: "consumer", Plugin: "consumer", AllowPending: true}
	if _, err = submit(ctx, h, gordis.Change{Upsert: []gordis.InstanceSpec{consumer}}); err != nil {
		return err
	}
	if err = expectConsumer(h, gordis.Pending); err != nil {
		return err
	}
	source := gordis.InstanceSpec{ID: "source", Plugin: "source"}
	if _, err = submit(ctx, h, gordis.Change{Upsert: []gordis.InstanceSpec{source}}); err != nil {
		return err
	}
	if err = h.WaitReady(ctx, "consumer"); err != nil {
		return err
	}
	if err = expectConsumer(h, gordis.Ready); err != nil {
		return err
	}
	removed, err := submit(ctx, h, gordis.Change{Remove: []string{"source"}, AllowWaitingConsumers: true})
	if err != nil {
		return err
	}
	if err = expectConsumer(h, gordis.Pending); err != nil {
		return err
	}
	if _, err = h.Restore(ctx, removed); err != nil {
		return err
	}
	if err = h.WaitReady(ctx, "consumer"); err != nil {
		return err
	}
	return expectConsumer(h, gordis.Ready)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
