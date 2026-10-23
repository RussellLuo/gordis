package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/stream/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/stream/contract"
	"github.com/RussellLuo/gordis/rpc/examples/stream/plugin"
)

func main() {
	err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{
		Identity: rpc.Identity{ID: "example/stream/program", Version: "1", Protocol: rpc.Protocol},
		Unit: rpc.Unit{
			Plugins:   []gordis.Plugin{new(plugin.Plugin)},
			Instances: []gordis.InstanceSpec{{ID: "generator", Plugin: contract.GeneratorPluginID}},
			Exports:   []rpc.Export{{Instance: "generator", Binding: bindings.Generator}},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
