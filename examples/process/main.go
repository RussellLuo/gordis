// This example attaches an independently built Greeter process to a running
// Host, calls its typed service, then stops and reaps the process.
//
// Build and run from the repository root as shown in examples/process/README.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/examples/process/contract"
	"github.com/RussellLuo/gordis/examples/process/contract/wire"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

type consumerPlugin struct{}

func (*consumerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "consumer",
		Requires: []gordis.ServiceSpec{contract.GreeterKey.Spec()},
		New:      func() gordis.Plugin { return new(consumerPlugin) },
	}
}

func (*consumerPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	greeter, err := gordis.Get(scope, contract.GreeterKey)
	if err != nil {
		return err
	}
	greeting, err := greeter.Greet(ctx, "Gordis")
	if err == nil {
		fmt.Println(greeting)
	}
	return err
}

func run(path string) error {
	var child *process.Client
	greeter, err := (processbridge.Adapter{
		ID:       "greeter",
		Provides: []processbridge.Binding{wire.Greeter},
		Resolve: func(json.RawMessage) (process.Options, error) {
			return process.Options{
				Path:     path,
				Identity: process.Identity{Protocol: process.Protocol, Package: contract.Package, Version: contract.Version},
			}, nil
		},
		OnClient: func(c *process.Client) { child = c },
	}).Plugin()
	if err != nil {
		return err
	}
	host, err := gordis.NewHost([]gordis.Plugin{greeter, new(consumerPlugin)})
	if err != nil {
		return err
	}
	// Reclaim Host resources if a later setup or runtime step fails.
	defer host.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Printf("host PID %d started without a plugin\n", os.Getpid())
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "greeter", Plugin: "greeter"}); err != nil {
		return err
	}
	fmt.Printf("plugin PID %d started\n", child.Snapshot().PID)

	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "consumer"}); err != nil {
		return err
	}

	if err := host.Shutdown(ctx); err != nil {
		return err
	}
	state := child.Snapshot()
	if !state.Reaped || !state.IOComplete || !state.HandlersComplete {
		return fmt.Errorf("plugin process was not fully reclaimed: %+v", state)
	}
	fmt.Println("plugin process reaped")
	return nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: process <plugin-executable>")
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}
