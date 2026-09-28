// Package notice implements a regular Gordis Plugin that publishes a typed
// event. It has no process-specific dependencies and can run locally or through
// the proc adapter.
package notice

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
)

const PluginID = "notice-publisher"

type Notice struct {
	Message string `json:"message"`
}

var Topic = events.NewTopic[Notice]("example.process-events.notice/1")

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.AfterReady("publish notice", func(ctx context.Context) error {
		return events.Bind(scope).Publish(ctx, Topic, Notice{Message: "hello"})
	})
}

var _ gordis.Plugin = (*Plugin)(nil)
