// Package host implements ordinary consumer and directory Plugins without RPC.
package host

import (
	"context"
	"fmt"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/contract"
)

const PluginID = "example/duplex/greeter-consumer"

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{contract.GreeterKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	greeter, err := scope.Get(contract.GreeterKey)
	if err != nil {
		return err
	}
	greeting, err := greeter.Greet(ctx, "user-42")
	if err != nil {
		return err
	}
	_, err = fmt.Println(greeting)
	return err
}
