// Package host implements an ordinary stream consumer without RPC.
package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/examples/stream/contract"
)

const PluginID = "example/stream/generator-consumer"

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{contract.GeneratorKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	generator, err := scope.Get(contract.GeneratorKey)
	if err != nil {
		return err
	}
	if err := generator.Generate(ctx, 3, func(chunk contract.Chunk) error {
		_, err := fmt.Printf("complete: %d\n", chunk.Index)
		return err
	}); err != nil {
		return err
	}

	stopped := errors.New("consumer stopped")
	err = generator.Generate(ctx, 5, func(chunk contract.Chunk) error {
		if _, err := fmt.Printf("partial: %d\n", chunk.Index); err != nil {
			return err
		}
		if chunk.Index == 2 {
			return stopped
		}
		return nil
	})
	if err == nil {
		return errors.New("stream completed without consumer stop")
	}
	if !errors.Is(err, stopped) {
		return err
	}
	_, err = fmt.Println(stopped)
	return err
}
