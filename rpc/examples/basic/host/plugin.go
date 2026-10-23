// Package host implements the ordinary consumer Plugin, independent of RPC.
package host

import (
	"context"
	"fmt"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/examples/basic/contract"
)

const PluginID = "example/basic/greeter-consumer"

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{contract.Key.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	service, err := scope.Get(contract.Key)
	if err != nil {
		return err
	}
	greeting, err := service.Greet(ctx, "Gordis")
	if err != nil {
		return err
	}
	_, err = fmt.Println(greeting)
	return err
}
