// This example attaches an independently built Greeter process to a running
// Host, calls its typed service, then stops and reaps the process.
//
// Build and run from the repository root as shown in examples/process/README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
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

type consumerPlugin struct{ service *contract.Greeter }

func (p *consumerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "consumer",
		Requires: []gordis.ServiceSpec{contract.GreeterKey.Spec()},
		New:      func() gordis.Plugin { return &consumerPlugin{service: p.service} },
	}
}

func (p *consumerPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	var err error
	*p.service, err = gordis.Get(scope, contract.GreeterKey)
	return err
}

func run(path string) (err error) {
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
	var service contract.Greeter
	consumer := &consumerPlugin{service: &service}
	host, err := gordis.NewHost([]gordis.Plugin{greeter, consumer}, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, host.Stop(context.Background())) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := host.Start(ctx); err != nil {
		return err
	}
	fmt.Printf("host PID %d started without a plugin\n", os.Getpid())
	plan, err := host.Preview(gordis.Change{Upsert: []gordis.InstanceSpec{
		{ID: "greeter", Plugin: "greeter"},
		{ID: "consumer", Plugin: "consumer"},
	}})
	if err != nil {
		return err
	}
	op, err := host.Apply(ctx, plan)
	if err != nil {
		return err
	}
	if err := host.WaitOperation(ctx, op); err != nil {
		return err
	}
	if err := host.WaitReady(ctx, "consumer"); err != nil {
		return err
	}
	greeting, err := service.Greet(ctx, "Gordis")
	if err != nil {
		return err
	}
	fmt.Printf("plugin PID %d: %s\n", child.Snapshot().PID, greeting)
	if err := host.Stop(ctx); err != nil {
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
