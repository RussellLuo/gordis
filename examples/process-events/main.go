// This example receives a typed event from a regular Notice Plugin running in
// a separately built process.
//
// Build and run from the repository root as shown in README.md.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/examples/process-events/notice"
	noticeproc "github.com/RussellLuo/gordis/examples/process-events/notice/proc"
	"github.com/RussellLuo/gordis/process"
)

type noticeObserverPlugin struct {
	received chan<- notice.Notice
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
	_, err := events.Bind(scope).On(notice.Topic, func(ctx context.Context, event notice.Notice) error {
		fmt.Printf("received notice: %s\n", event.Message)
		select {
		case p.received <- event:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return err
}

func run(path string) error {
	received := make(chan notice.Notice, 1)
	remotePublisher, err := noticeproc.New(func() (process.Options, error) {
		return process.Options{Path: path}, nil
	})
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

	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "events", Plugin: events.PluginID}); err != nil {
		return err
	}
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "notice-observer", Plugin: "notice-observer"}); err != nil {
		return err
	}
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "notice-publisher", Plugin: notice.PluginID}); err != nil {
		return err
	}

	select {
	case <-received:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
