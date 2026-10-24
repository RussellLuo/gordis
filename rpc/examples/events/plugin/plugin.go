// Package plugin implements an ordinary event Plugin without RPC.
package plugin

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc/examples/events/contract"
)

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       contract.PluginID,
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	bus := events.Bind(scope)
	_, err := bus.On(contract.RequestTopic, func(ctx context.Context, request contract.Request) error {
		return bus.Publish(ctx, contract.NoticeTopic, contract.Notice{Message: request.Message})
	})
	return err
}
