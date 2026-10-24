// Package host implements an ordinary observer and requester without RPC.
package host

import (
	"context"
	"fmt"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc/examples/events/contract"
)

const PluginID = "example/events/observer"

type Plugin struct {
	Completed chan<- error // Caller supplies a buffer for the single Emit result.
}

func (p *Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		New: func() gordis.Plugin {
			return &Plugin{Completed: p.Completed}
		},
	}
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	bus := events.Bind(scope)
	if _, err := bus.On(contract.NoticeTopic, func(_ context.Context, notice contract.Notice) error {
		_, err := fmt.Printf("received notice: %s\n", notice.Message)
		return err
	}); err != nil {
		return err
	}
	return scope.AfterReady("request notice", func(ctx context.Context) error {
		err := bus.Emit(ctx, contract.RequestTopic, contract.Request{Message: "hello"})
		p.Completed <- err
		return err
	})
}
