// This example wraps processbridge behind the Greeter proc package, so the
// Host registers the remote Plugin almost like a local Plugin.
//
// Build and run from the repository root as shown in README.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/examples/process/greeter"
	greeterproc "github.com/RussellLuo/gordis/examples/process/greeter/proc"
	"github.com/RussellLuo/gordis/process"
)

type consumerPlugin struct{}

func (*consumerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "consumer",
		Requires: []gordis.ServiceSpec{greeter.Key.Spec()},
		New:      func() gordis.Plugin { return new(consumerPlugin) },
	}
}

func (*consumerPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	service, err := gordis.Get(scope, greeter.Key)
	if err != nil {
		return err
	}
	greeting, err := service.Greet(ctx, "Gordis")
	if err == nil {
		fmt.Println(greeting)
	}
	return err
}

func run(path string) error {
	remoteGreeter, err := greeterproc.New(func() (process.Options, error) {
		return process.Options{Path: path}, nil
	})
	if err != nil {
		return err
	}

	host, err := gordis.NewHost(remoteGreeter, new(consumerPlugin))
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID:     "greeter",
		Plugin: greeter.PluginID,
		Config: json.RawMessage(`{"prefix":"Hello"}`),
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
		log.Fatal("usage: process <plugin-executable>")
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

var _ gordis.Plugin = (*consumerPlugin)(nil)
