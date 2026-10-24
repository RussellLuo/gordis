package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/events/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/events/contract"
	"github.com/RussellLuo/gordis/rpc/examples/events/plugin"
)

func run() error {
	bridge, err := bindings.NewBridge()
	if err != nil {
		return err
	}
	unit, err := bridge.Unit(rpc.Unit{
		Plugins:   []gordis.Plugin{new(plugin.Plugin)},
		Instances: []gordis.InstanceSpec{{ID: "publisher", Plugin: contract.PluginID}},
	})
	if err != nil {
		return err
	}
	return rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{
		Identity: rpc.Identity{ID: "example/events/program", Version: "1", Protocol: rpc.Protocol},
		Unit:     unit,
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
