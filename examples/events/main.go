// This example publishes a typed event to a subscriber.
//
// Run from the repository root: go run ./examples/events
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
)

type orderCreated struct {
	ID string
}

var orderCreatedTopic = events.NewTopic[orderCreated]("example.order-created/1")

type orderObserverPlugin struct {
	received chan<- orderCreated
}

func (p *orderObserverPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "order-observer",
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		New: func() gordis.Plugin {
			return &orderObserverPlugin{received: p.received}
		},
	}
}

func (p *orderObserverPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	_, err := events.Bind(scope).On(orderCreatedTopic, func(ctx context.Context, event orderCreated) error {
		fmt.Printf("received order: %s\n", event.ID)
		select {
		case p.received <- event:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return err
}

type orderPublisherPlugin struct{}

func (*orderPublisherPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "order-publisher",
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		New:      func() gordis.Plugin { return new(orderPublisherPlugin) },
	}
}

func (*orderPublisherPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.AfterReady("publish order", func(ctx context.Context) error {
		return events.Bind(scope).Publish(ctx, orderCreatedTopic, orderCreated{ID: "order-42"})
	})
}

func run() error {
	received := make(chan orderCreated, 1)
	host, err := gordis.NewHost([]gordis.Plugin{
		new(events.Plugin),
		&orderObserverPlugin{received: received},
		new(orderPublisherPlugin),
	})
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "events", Plugin: events.PluginID,
	}); err != nil {
		return err
	}
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "order-observer", Plugin: "order-observer",
	}); err != nil {
		return err
	}
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "order-publisher", Plugin: "order-publisher",
	}); err != nil {
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
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

var _ gordis.Plugin = (*orderObserverPlugin)(nil)
var _ gordis.Plugin = (*orderPublisherPlugin)(nil)
