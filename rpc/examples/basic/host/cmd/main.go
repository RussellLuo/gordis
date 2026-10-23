package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/basic/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/basic/contract"
	consumer "github.com/RussellLuo/gordis/rpc/examples/basic/host"
)

func run(path string, config json.RawMessage) (err error) {
	provider, err := rpc.NewProcess(rpc.ProcessOptions{
		PluginID:      contract.PluginID,
		Command:       rpc.Command{Path: path},
		Identity:      rpc.Identity{ID: "example/basic/program", Version: "1", Protocol: rpc.Protocol},
		ChildBoundary: rpc.Boundary{Exports: []rpc.Binding{bindings.Greeter}},
	})
	if err != nil {
		return err
	}
	host, err := gordis.NewHost(provider, new(consumer.Plugin))
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, host.Shutdown(ctx))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "greeter", Plugin: contract.PluginID, Config: config,
	}); err != nil {
		return err
	}
	_, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: consumer.PluginID})
	return err
}

func main() {
	config := flag.String("config", `{}`, "Program configuration as a JSON object")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: basic [-config JSON] <plugin-executable>")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), json.RawMessage(*config)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
