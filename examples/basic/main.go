// This example shows typed services and per-instance configuration. One greeter
// serves two greeting instances configured for Alice and Bob.
//
// Run from the repository root: go run ./examples/basic
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/RussellLuo/gordis"
)

type Greeter interface{ Greet(string) string }

var greeterKey = gordis.NewKey[Greeter]("example.greeter")

type greeterService struct{}

func (*greeterService) Greet(name string) string { return "Hello, " + name }

type greeterPlugin struct{}

func (*greeterPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "greeter",
		Provides: []gordis.ServiceSpec{greeterKey.Spec()},
		New:      func() gordis.Plugin { return new(greeterPlugin) },
	}
}

func (*greeterPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide[Greeter](scope, greeterKey, new(greeterService))
}

type greetingPlugin struct {
	Name string `json:"name"`
}

func (*greetingPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "greeting",
		Requires: []gordis.ServiceSpec{greeterKey.Spec()},
		New:      func() gordis.Plugin { return new(greetingPlugin) },
	}
}

func (p *greetingPlugin) Validate() error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

func (p *greetingPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	greeter, err := gordis.Get(scope, greeterKey)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", scope.ID(), greeter.Greet(p.Name))
	return nil
}

func run() error {
	h, err := gordis.NewHost(new(greeterPlugin), new(greetingPlugin))
	if err != nil {
		return err
	}
	defer h.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err = h.Env().MountReady(ctx, gordis.InstanceSpec{ID: "greeter", Plugin: "greeter"}); err != nil {
		return err
	}

	if _, err = h.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "alice", Plugin: "greeting", Config: json.RawMessage(`{"name":"Alice"}`),
	}); err != nil {
		return err
	}
	if _, err = h.Env().MountReady(ctx, gordis.InstanceSpec{
		ID: "bob", Plugin: "greeting", Config: json.RawMessage(`{"name":"Bob"}`),
	}); err != nil {
		return err
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

var _ gordis.Plugin = (*greeterPlugin)(nil)
var _ gordis.Plugin = (*greetingPlugin)(nil)
var _ gordis.Validator = (*greetingPlugin)(nil)
var _ Greeter = (*greeterService)(nil)
