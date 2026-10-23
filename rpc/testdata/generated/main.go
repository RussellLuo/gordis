package main

import (
	"context"
	"fmt"
	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/bindings"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/business"
	"os"
)

type Config struct {
	Prefix string `json:"prefix,omitempty"`
}

func (Config) New() Config { return Config{Prefix: "Hello"} }
func (c Config) Configure(context.Context) (map[string]any, error) {
	return map[string]any{"business": map[string]string{"prefix": c.Prefix}}, nil
}
func main() {
	config, err := rpc.NewConfig[Config]()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	u := rpc.Unit{Plugins: []gordis.Plugin{&business.Plugin{}}, Instances: []gordis.InstanceSpec{{ID: "business", Plugin: "genfixture/business"}}, Config: config, Exports: []rpc.Export{{Instance: "business", Binding: bindings.Service}}}
	if err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: rpc.Identity{ID: "genfixture/program", Version: "1", Protocol: rpc.Protocol}, Unit: u}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
