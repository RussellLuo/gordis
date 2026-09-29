// This example shows that ownership controls cleanup, while a Service
// dependency controls readiness.
//
// Run from the repository root: go run ./examples/readiness
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/RussellLuo/gordis"
)

type metricsBackend interface{ Name() string }
type backendValue string

func (b backendValue) Name() string { return string(b) }

var metricsBackendKey = gordis.NewKey[metricsBackend]("example.metrics-backend/1")

type backendPlugin struct{}

func (*backendPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "metrics-backend",
		Provides: []gordis.ServiceSpec{metricsBackendKey.Spec()},
		New:      func() gordis.Plugin { return new(backendPlugin) },
	}
}

func (*backendPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide[metricsBackend](scope, metricsBackendKey, backendValue("prometheus"))
}

type metricsPlugin struct{}

func (*metricsPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "metrics",
		Requires: []gordis.ServiceSpec{metricsBackendKey.Spec()},
		New:      func() gordis.Plugin { return new(metricsPlugin) },
	}
}

func (*metricsPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	backend, err := gordis.Get(scope, metricsBackendKey)
	if err != nil {
		return err
	}
	fmt.Printf("metrics ready: backend=%s\n", backend.Name())
	return nil
}

type collectorPlugin struct{}

func (*collectorPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:  "collector",
		New: func() gordis.Plugin { return new(collectorPlugin) },
	}
}

func (*collectorPlugin) Activate(context.Context, *gordis.Scope) error { return nil }

func run(ctx context.Context) error {
	host, err := gordis.NewHost(
		new(collectorPlugin),
		new(metricsPlugin),
		new(backendPlugin),
	)
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	collector, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "collector", Plugin: "collector"})
	if err != nil {
		return err
	}
	metrics, err := collector.Env().Mount(ctx, gordis.InstanceSpec{ID: "metrics", Plugin: "metrics"})
	if err != nil {
		return err
	}
	fmt.Println("collector ready; metrics pending")

	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "backend", Plugin: "metrics-backend"}); err != nil {
		return err
	}
	return metrics.WaitReady(ctx)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
