// This example shows a process Greeter providing one typed Service while using
// a UserDirectory Service from the Host process.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/examples/duplex/greeter"
	greeterproc "github.com/RussellLuo/gordis/examples/duplex/greeter/proc"
	"github.com/RussellLuo/gordis/process"
)

type directoryPlugin struct{}

func (*directoryPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "user-directory",
		Provides: []gordis.ServiceSpec{greeter.UserDirectoryKey.Spec()},
		New:      func() gordis.Plugin { return new(directoryPlugin) },
	}
}

func (*directoryPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide(scope, greeter.UserDirectoryKey, greeter.UserDirectory(directoryService{}))
}

type directoryService struct{}

func (directoryService) DisplayName(_ context.Context, userID string) (string, error) {
	fmt.Printf("process Greeter → Host UserDirectory: displayName(%q)\n", userID)
	return "Gordis", nil
}

type consumerPlugin struct{}

func (*consumerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "consumer",
		Requires: []gordis.ServiceSpec{greeter.GreeterKey.Spec()},
		New:      func() gordis.Plugin { return new(consumerPlugin) },
	}
}

func (*consumerPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	service, err := gordis.Get(scope, greeter.GreeterKey)
	if err != nil {
		return err
	}
	fmt.Println(`Host consumer → process Greeter: greet("user-42")`)
	greeting, err := service.Greet(ctx, "user-42")
	if err != nil {
		return err
	}
	fmt.Printf("result: %s\n", greeting)
	return nil
}

func run(path string) error {
	remoteGreeter, err := greeterproc.New(func() (process.Options, error) {
		return process.Options{Path: path}, nil
	})
	if err != nil {
		return err
	}

	host, err := gordis.NewHost([]gordis.Plugin{
		new(directoryPlugin), remoteGreeter, new(consumerPlugin),
	})
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "directory", Plugin: "user-directory",
	}); err != nil {
		return err
	}
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "greeter", Plugin: greeter.PluginID,
	}); err != nil {
		return err
	}
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "consumer"}); err != nil {
		return err
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: duplex <plugin-executable>")
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

var _ gordis.Plugin = (*directoryPlugin)(nil)
var _ gordis.Plugin = (*consumerPlugin)(nil)
var _ greeter.UserDirectory = directoryService{}
