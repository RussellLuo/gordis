// Package proc implements the optional process boundary for greeter.
package proc

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/examples/duplex/greeter"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

type Resolver func() (process.Options, error)

var identity = process.Identity{
	Protocol: process.Protocol,
	Package:  "example-duplex-greeter",
	Version:  "1.0.0",
}

func New(resolve Resolver) (gordis.Plugin, error) {
	if resolve == nil {
		return nil, errors.New("greeter/proc: nil resolver")
	}

	return (processbridge.Adapter{
		ID:       greeter.PluginID,
		Requires: []processbridge.Binding{userDirectoryBinding},
		Provides: []processbridge.Binding{greeterBinding},
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

func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser) error {
	return processbridge.Serve(ctx, in, out, processbridge.ServeOptions{
		Identity: identity,
		Plugin:   new(greeter.Plugin),
		Requires: []processbridge.Binding{userDirectoryBinding},
		Provides: []processbridge.Binding{greeterBinding},
	})
}

type userDirectoryClient struct {
	caller process.Caller
}

func (c userDirectoryClient) DisplayName(ctx context.Context, userID string) (string, error) {
	var name string
	err := c.caller.Call(ctx, "displayName", userID, &name)
	return name, err
}

type greeterClient struct {
	caller process.Caller
}

func (c greeterClient) Greet(ctx context.Context, userID string) (string, error) {
	var greeting string
	err := c.caller.Call(ctx, "greet", userID, &greeting)
	return greeting, err
}

var userDirectoryBinding = processbridge.Bind(
	greeter.UserDirectoryKey,
	func(caller process.Caller) greeter.UserDirectory { return userDirectoryClient{caller: caller} },
	func(service greeter.UserDirectory) process.CallHandler {
		return func(ctx context.Context, _ process.Caller, method string, raw json.RawMessage) (any, error) {
			if method != "displayName" {
				return nil, &process.RPCError{Code: "method", Message: "unknown user directory method"}
			}
			var userID string
			if err := json.Unmarshal(raw, &userID); err != nil {
				return nil, err
			}
			return service.DisplayName(ctx, userID)
		}
	},
)

var greeterBinding = processbridge.Bind(
	greeter.GreeterKey,
	func(caller process.Caller) greeter.Greeter { return greeterClient{caller: caller} },
	func(service greeter.Greeter) process.CallHandler {
		return func(ctx context.Context, _ process.Caller, method string, raw json.RawMessage) (any, error) {
			if method != "greet" {
				return nil, &process.RPCError{Code: "method", Message: "unknown greeter method"}
			}
			var userID string
			if err := json.Unmarshal(raw, &userID); err != nil {
				return nil, err
			}
			return service.Greet(ctx, userID)
		}
	},
)

var _ greeter.UserDirectory = userDirectoryClient{}
var _ greeter.Greeter = greeterClient{}
