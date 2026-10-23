// Package plugin implements an ordinary Generator Plugin without RPC.
package plugin

import (
	"context"
	"errors"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/examples/stream/contract"
)

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       contract.GeneratorPluginID,
		Provides: []gordis.ServiceSpec{contract.GeneratorKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide[contract.Generator](contract.GeneratorKey, generator{})
}

type generator struct{}

func (generator) Generate(ctx context.Context, count int, yield func(contract.Chunk) error) error {
	if count < 0 {
		return errors.New("count must be nonnegative")
	}
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(contract.Chunk{Index: i + 1}); err != nil {
			return err
		}
	}
	return nil
}
