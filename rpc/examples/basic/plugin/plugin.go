// Package plugin implements an ordinary Plugin, independent of RPC.
package plugin

import (
	"context"
	"errors"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/examples/basic/contract"
)

type Plugin struct {
	Prefix string `json:"prefix"`
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       contract.PluginID,
		Provides: []gordis.ServiceSpec{contract.Key.Spec()},
		New:      func() gordis.Plugin { return &Plugin{Prefix: "Hello"} },
	}
}

func (p *Plugin) Validate() error {
	if p.Prefix == "" {
		return errors.New("prefix is required")
	}
	return nil
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide[contract.Greeter](contract.Key, greeter{prefix: p.Prefix})
}

type greeter struct{ prefix string }

func (g greeter) Greet(_ context.Context, name string) (string, error) {
	return g.prefix + ", " + name + "!", nil
}
