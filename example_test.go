package gordis_test

import (
	"context"
	"fmt"

	"github.com/RussellLuo/gordis"
)

type exampleGreeter struct{}

func (*exampleGreeter) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:  "greeter",
		New: func() gordis.Plugin { return new(exampleGreeter) },
	}
}

func (*exampleGreeter) Activate(_ context.Context, scope *gordis.Scope) error {
	fmt.Printf("%s: Hello, Gordis!\n", scope.ID())
	return nil
}

func ExampleNewHost() {
	host, err := gordis.NewHost([]gordis.Plugin{new(exampleGreeter)})
	if err != nil {
		panic(err)
	}
	defer host.Shutdown(context.Background())

	instance, err := host.Env().MountReady(context.Background(), gordis.InstanceSpec{
		ID: "greeter", Plugin: "greeter",
	})
	if err != nil {
		panic(err)
	}
	if err := instance.Unmount(context.Background()); err != nil {
		panic(err)
	}

	// Output:
	// greeter: Hello, Gordis!
}
