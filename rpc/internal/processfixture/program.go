package processfixture

import (
	"context"
	"encoding/json"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture/contract"
)

type TextClient struct{ Invoker rpc.Invoker }

func (c *TextClient) Call(ctx context.Context, input string) (string, error) {
	var value string
	err := c.Invoker.Call(ctx, "call", input, &value)
	return value, err
}
func TextCodec() rpc.Codec[contract.Text] {
	return rpc.Codec[contract.Text]{
		Client: func(invoker rpc.Invoker) contract.Text { return &TextClient{invoker} },
		Server: func(service contract.Text) rpc.Handler {
			return rpc.Handler{Methods: map[string]rpc.Unary{"call": func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				var input string
				if err := json.Unmarshal(raw, &input); err != nil {
					return nil, err
				}
				value, err := service.Call(ctx, input)
				if err != nil {
					return nil, err
				}
				return json.Marshal(value)
			}}}
		},
	}
}

type NumberClient struct{ Invoker rpc.Invoker }

func (c *NumberClient) Value(ctx context.Context) (int, error) {
	var value int
	err := c.Invoker.Call(ctx, "value", nil, &value)
	return value, err
}
func NumberCodec() rpc.Codec[contract.Number] {
	return rpc.Codec[contract.Number]{
		Client: func(invoker rpc.Invoker) contract.Number { return &NumberClient{invoker} },
		Server: func(service contract.Number) rpc.Handler {
			return rpc.Handler{Methods: map[string]rpc.Unary{"value": func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
				value, err := service.Value(ctx)
				if err != nil {
					return nil, err
				}
				return json.Marshal(value)
			}}}
		},
	}
}
func Identity() rpc.Identity {
	return rpc.Identity{ID: "processfixture/program", Version: "1", Protocol: rpc.Protocol}
}
func Boundary(mode string) rpc.Boundary {
	var boundary rpc.Boundary
	if mode == "duplex" || mode == "import-only" {
		boundary.Imports = []rpc.Binding{rpc.NewBinding(contract.CallbackKey, "1", TextCodec())}
	}
	if mode == "single" || mode == "multi" || mode == "duplex" {
		boundary.Exports = []rpc.Binding{rpc.NewBinding(contract.TextKey, "1", TextCodec())}
	}
	if mode == "multi" {
		boundary.Exports = append(boundary.Exports, rpc.NewBinding(contract.NumberKey, "1", NumberCodec()))
	}
	return boundary
}
