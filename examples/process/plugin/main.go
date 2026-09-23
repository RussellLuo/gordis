// This executable serves a Greeter plugin in a separate process. The
// Host builds independently and communicates with it through a typed binding.
//
// Build and run from the repository root as shown in examples/process/README.md.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/examples/process/contract"
	"github.com/RussellLuo/gordis/examples/process/contract/wire"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

type greeter struct{}

func (greeter) Greet(_ context.Context, name string) (string, error) {
	return "Hello, " + name + "!", nil
}

type greeterPlugin struct{}

func (*greeterPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "greeter",
		Provides: []gordis.ServiceSpec{contract.GreeterKey.Spec()},
		New:      func() gordis.Plugin { return new(greeterPlugin) },
	}
}

func (*greeterPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide(scope, contract.GreeterKey, contract.Greeter(greeter{}))
}

func main() {
	err := processbridge.Serve(context.Background(), os.Stdin, os.Stdout, processbridge.ServeOptions{
		Identity: process.Identity{Protocol: process.Protocol, Package: contract.Package, Version: contract.Version},
		Plugin:   new(greeterPlugin),
		Provides: []processbridge.Binding{wire.Greeter},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
