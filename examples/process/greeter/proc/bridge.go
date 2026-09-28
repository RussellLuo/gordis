// Package proc implements the optional process boundary for greeter.
// The business Plugin remains a normal Local-first Gordis Plugin.
package proc

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/examples/process/greeter"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

// Resolver supplies application-owned process settings such as the executable
// path. New adds the protocol identity and per-instance business configuration.
type Resolver func() (process.Options, error)

var identity = process.Identity{
	Protocol: process.Protocol,
	Package:  "example-greeter",
	Version:  "1.0.0",
}

// New returns the process-backed prototype registered with a Host.
func New(resolve Resolver) (gordis.Plugin, error) {
	if resolve == nil {
		return nil, errors.New("greeter/proc: nil resolver")
	}

	return (processbridge.Adapter{
		ID:       greeter.PluginID,
		Provides: []processbridge.Binding{binding},
		Resolve: func(raw json.RawMessage) (process.Options, error) {
			resolved, err := resolve()
			if err != nil {
				return process.Options{}, err
			}
			resolved.Identity = identity
			resolved.Config = append(json.RawMessage(nil), raw...)
			return resolved, nil
		},
	}).Plugin()
}

// Serve runs the same business Plugin in a child process.
func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser) error {
	return processbridge.Serve(ctx, in, out, processbridge.ServeOptions{
		Identity: identity,
		Plugin:   new(greeter.Plugin),
		Provides: []processbridge.Binding{binding},
	})
}

type client struct {
	caller process.Caller
}

func (c client) Greet(ctx context.Context, name string) (string, error) {
	var greeting string
	err := c.caller.Call(ctx, "greet", name, &greeting)
	return greeting, err
}

var binding = processbridge.Bind(
	greeter.Key,
	func(caller process.Caller) greeter.Greeter {
		return client{caller: caller}
	},
	func(service greeter.Greeter) process.CallHandler {
		return func(ctx context.Context, _ process.Caller, method string, raw json.RawMessage) (any, error) {
			if method != "greet" {
				return nil, &process.RPCError{Code: "method", Message: "unknown greeter method"}
			}
			var name string
			if err := json.Unmarshal(raw, &name); err != nil {
				return nil, err
			}
			return service.Greet(ctx, name)
		}
	},
)

var _ greeter.Greeter = client{}
