package wire

import (
	"context"
	"encoding/json"

	"github.com/RussellLuo/gordis/examples/process/contract"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

type greeterClient struct{ caller process.Caller }

func (c greeterClient) Greet(ctx context.Context, name string) (string, error) {
	var greeting string
	err := c.caller.Call(ctx, "greet", name, &greeting)
	return greeting, err
}

var Greeter = processbridge.Bind(contract.GreeterKey,
	func(c process.Caller) contract.Greeter { return greeterClient{c} },
	func(g contract.Greeter) process.CallHandler {
		return func(ctx context.Context, _ process.Caller, method string, raw json.RawMessage) (any, error) {
			if method != "greet" {
				return nil, &process.RPCError{Code: "method", Message: "unknown greeter method"}
			}
			var name string
			if err := json.Unmarshal(raw, &name); err != nil {
				return nil, err
			}
			return g.Greet(ctx, name)
		}
	})
