package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/contract"
	consumer "github.com/RussellLuo/gordis/rpc/examples/duplex/host"
)

func run(path string) (err error) {
	provider, err := rpc.NewProcess(rpc.ProcessOptions{
		PluginID: contract.GreeterPluginID,
		Command:  rpc.Command{Path: path},
		Identity: rpc.Identity{ID: "example/duplex/program", Version: "1", Protocol: rpc.Protocol},
		ChildBoundary: rpc.Boundary{
			Imports: []rpc.Binding{bindings.UserDirectory},
			Exports: []rpc.Binding{bindings.Greeter},
		},
	})
	if err != nil {
		return err
	}
	host, err := gordis.NewHost(new(consumer.DirectoryPlugin), provider, new(consumer.Plugin))
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
	for _, instance := range []gordis.InstanceSpec{
		{ID: "directory", Plugin: contract.DirectoryPluginID},
		{ID: "greeter", Plugin: contract.GreeterPluginID},
		{ID: "consumer", Plugin: consumer.PluginID},
	} {
		if _, err := host.Env().MountReady(ctx, instance); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: duplex <plugin-executable>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
