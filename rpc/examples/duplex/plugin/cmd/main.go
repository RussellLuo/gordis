package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/contract"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/plugin"
)

func main() {
	err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{
		Identity: rpc.Identity{ID: "example/duplex/program", Version: "1", Protocol: rpc.Protocol},
		Unit: rpc.Unit{
			Plugins:   []gordis.Plugin{new(plugin.Plugin)},
			Instances: []gordis.InstanceSpec{{ID: "greeter", Plugin: contract.GreeterPluginID}},
			Imports:   []rpc.Binding{bindings.UserDirectory},
			Exports:   []rpc.Export{{Instance: "greeter", Binding: bindings.Greeter}},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
