// This example shows an optional child failing without stopping its parent.
// The metrics child cleans up, while the collector remains ready; the app
// checks Snapshot.GroupReady before treating the collector as available.
//
// Run from the repository root: go run ./examples/optional
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/RussellLuo/gordis"
)

type collectorPlugin struct{}

func (*collectorPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "collector", New: func() gordis.Plugin { return new(collectorPlugin) }}
}

func (*collectorPlugin) Start(context.Context, *gordis.Scope) error { return nil }

type metricsPlugin struct{}

func (*metricsPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "metrics", New: func() gordis.Plugin { return new(metricsPlugin) }}
}

func (*metricsPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("metrics resource", func(context.Context) error {
		fmt.Println("metrics: resource released")
		return nil
	}); err != nil {
		return err
	}
	return errors.New("metrics backend unavailable")
}

func run() (err error) {
	h, err := gordis.NewHost([]gordis.Plugin{new(collectorPlugin), new(metricsPlugin)}, []gordis.InstanceSpec{
		{ID: "collector", Plugin: "collector"},
		{ID: "metrics", Plugin: "metrics", Parent: "collector", Optional: true},
	})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, h.Stop(ctx))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	startErr := h.Start(ctx)
	if startErr != nil {
		// Report the error, then check whether the application's main group is usable.
		fmt.Printf("Start: %v\n", startErr)
	}
	var usable bool
	for _, snapshot := range h.Snapshot() {
		fmt.Printf("%s: state=%s, optional=%t, groupReady=%t, cleanupComplete=%t\n",
			snapshot.ID, snapshot.State, snapshot.Optional, snapshot.GroupReady, snapshot.CleanupComplete)
		if snapshot.ID == "collector" {
			usable = snapshot.GroupReady
		}
	}
	if !usable {
		return errors.Join(startErr, errors.New("collector group is not ready"))
	}
	fmt.Println("collector: main functionality remains available")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
