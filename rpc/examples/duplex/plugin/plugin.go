// Package plugin implements an ordinary Greeter Plugin without RPC.
package plugin

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/contract"
)

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       contract.GreeterPluginID,
		Requires: []gordis.ServiceSpec{contract.DirectoryKey.Spec()},
		Provides: []gordis.ServiceSpec{contract.GreeterKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	directory, err := scope.Get(contract.DirectoryKey)
	if err != nil {
		return err
	}
	return scope.Provide[contract.Greeter](contract.GreeterKey, greeter{directory})
}

type greeter struct{ directory contract.UserDirectory }

func (g greeter) Greet(ctx context.Context, userID string) (string, error) {
	name, err := g.directory.DisplayName(ctx, userID)
	if err != nil {
		return "", err
	}
	return "Hello, " + name + "!", nil
}
