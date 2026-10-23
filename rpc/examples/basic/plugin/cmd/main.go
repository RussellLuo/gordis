package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/basic/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/basic/contract"
	"github.com/RussellLuo/gordis/rpc/examples/basic/plugin"
)

type Config struct {
	Prefix string `json:"prefix,omitempty" jsonschema:"Greeting prefix" jsonschemax:"minLength=1"`
}

func (Config) New() Config { return Config{Prefix: "Hello"} }
func (c Config) Configure(context.Context) (map[string]any, error) {
	return map[string]any{"greeter": c}, nil
}

func run(ctx context.Context) error {
	config, err := rpc.NewConfig[Config]()
	if err != nil {
		return err
	}
	return rpc.ServeUnit(ctx, rpc.ServeUnitOptions{
		Identity: rpc.Identity{ID: "example/basic/program", Version: "1", Protocol: rpc.Protocol},
		Unit: rpc.Unit{
			Plugins:   []gordis.Plugin{new(plugin.Plugin)},
			Instances: []gordis.InstanceSpec{{ID: "greeter", Plugin: contract.PluginID}},
			Config:    config,
			Exports:   []rpc.Export{{Instance: "greeter", Binding: bindings.Greeter}},
		},
	})
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
