// This executable publishes a typed event from a separate process.
//
// Build and run from the repository root as shown in
// examples/process-events/README.md.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/examples/process-events/contract"
	"github.com/RussellLuo/gordis/examples/process-events/contract/wire"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

type remotePublisherPlugin struct{}

func (*remotePublisherPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "remote-publisher",
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		New:      func() gordis.Plugin { return new(remotePublisherPlugin) },
	}
}

func (*remotePublisherPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.AfterReady("publish notice", func(ctx context.Context) error {
		return events.Bind(scope).Publish(ctx, contract.NoticeTopic, contract.Notice{Message: "hello"})
	})
}

func main() {
	err := processbridge.Serve(context.Background(), os.Stdin, os.Stdout, processbridge.ServeOptions{
		Identity: process.Identity{
			Protocol: process.Protocol,
			Package:  contract.Package,
			Version:  contract.Version,
		},
		Plugin: new(remotePublisherPlugin),
		Events: []processbridge.EventBinding{wire.Notice},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var _ gordis.Plugin = (*remotePublisherPlugin)(nil)
