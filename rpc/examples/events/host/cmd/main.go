package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/events/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/events/contract"
	observer "github.com/RussellLuo/gordis/rpc/examples/events/host"
)

func run(path string) (err error) {
	bridge, err := bindings.NewBridge()
	if err != nil {
		return err
	}
	process, err := bridge.NewProcess(rpc.ProcessOptions{
		PluginID: contract.PluginID,
		Command:  rpc.Command{Path: path},
		Identity: rpc.Identity{ID: "example/events/program", Version: "1", Protocol: rpc.Protocol},
	})
	if err != nil {
		return err
	}
	completed := make(chan error, 1)
	host, err := gordis.NewHost(new(events.Plugin), process, &observer.Plugin{Completed: completed})
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
		{ID: "events", Plugin: events.PluginID},
		{ID: "publisher", Plugin: contract.PluginID},
		{ID: "observer", Plugin: observer.PluginID},
	} {
		if _, err := host.Env().MountReady(ctx, instance); err != nil {
			return err
		}
	}
	select {
	case err := <-completed:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: events <plugin-executable>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
