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

	if _, err := host.Env().MountReady(context.Background(), gordis.InstanceSpec{
		ID: "greeter", Plugin: "greeter",
	}); err != nil {
		panic(err)
	}

	// Output:
	// greeter: Hello, Gordis!
}
