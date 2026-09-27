// This example receives a typed event published by a separately built process
// plugin, then stops and reaps that process.
//
// Build and run from the repository root as shown in
// examples/process-events/README.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/examples/process-events/contract"
	"github.com/RussellLuo/gordis/examples/process-events/contract/wire"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

type noticeObserverPlugin struct {
	received chan<- contract.Notice
}

func (p *noticeObserverPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "notice-observer",
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		New: func() gordis.Plugin {
			return &noticeObserverPlugin{received: p.received}
		},
	}
}

func (p *noticeObserverPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	_, err := events.Bind(scope).On(contract.NoticeTopic, func(ctx context.Context, notice contract.Notice) error {
		fmt.Printf("received from process: %s\n", notice.Message)
		select {
		case p.received <- notice:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return err
}

func run(path string) error {
	received := make(chan contract.Notice, 1)
	var child *process.Client
	remotePublisher, err := (processbridge.Adapter{
		ID:     "remote-publisher",
		Events: []processbridge.EventBinding{wire.Notice},
		Resolve: func(json.RawMessage) (process.Options, error) {
			return process.Options{
				Path: path,
				Identity: process.Identity{
					Protocol: process.Protocol,
					Package:  contract.Package,
					Version:  contract.Version,
				},
			}, nil
		},
		OnClient: func(c *process.Client) { child = c },
	}).Plugin()
	if err != nil {
		return err
	}

	host, err := gordis.NewHost([]gordis.Plugin{
		new(events.Plugin),
		&noticeObserverPlugin{received: received},
		remotePublisher,
	})
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "events", Plugin: events.PluginID,
	}); err != nil {
		return err
	}
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "notice-observer", Plugin: "notice-observer",
	}); err != nil {
		return err
	}
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "remote-publisher", Plugin: "remote-publisher",
	}); err != nil {
		return err
	}

	select {
	case <-received:
	case <-ctx.Done():
		return ctx.Err()
	}

	if err = host.Shutdown(ctx); err != nil {
		return err
	}
	if child == nil {
		return fmt.Errorf("plugin process was not started")
	}
	state := child.Snapshot()
	if !state.Reaped || !state.IOComplete || !state.HandlersComplete {
		return fmt.Errorf("plugin process was not fully reclaimed: %+v", state)
	}
	fmt.Println("plugin process reaped")
	return nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: process-events <plugin-executable>")
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

var _ gordis.Plugin = (*noticeObserverPlugin)(nil)
