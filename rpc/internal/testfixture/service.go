// Package testfixture supplies ordinary services for the library's process tests.
package testfixture

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
)

type Service interface {
	Call(context.Context, string) (string, error)
	Stream(context.Context, func(json.RawMessage) error) error
	Validate(context.Context) error
}

var Key = gordis.NewKey[Service]("test/service")
var CallbackKey = gordis.NewKey[Service]("test/callback")

type client struct{ rpc.Invoker }

func (c *client) Call(ctx context.Context, input string) (string, error) {
	var output string
	err := c.Invoker.Call(ctx, "call", input, &output)
	return output, err
}
func (c *client) Stream(ctx context.Context, yield func(json.RawMessage) error) error {
	return c.Invoker.Stream(ctx, "stream", nil, yield)
}
func (*client) Validate(context.Context) error { return nil }
func Binding(key gordis.Key[Service]) rpc.Binding {
	return rpc.NewBinding(key, "1", rpc.Codec[Service]{
		Client:   func(invoker rpc.Invoker) Service { return &client{invoker} },
		Validate: func(ctx context.Context, service Service) error { return service.Validate(ctx) },
		Server: func(service Service) rpc.Handler {
			return rpc.Handler{
				Methods: map[string]rpc.Unary{"call": func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
					var input string
					if err := json.Unmarshal(raw, &input); err != nil {
						return nil, err
					}
					output, err := service.Call(ctx, input)
					if err != nil {
						return nil, err
					}
					return json.Marshal(output)
				}},
				Streams: map[string]rpc.Stream{"stream": func(ctx context.Context, _ json.RawMessage, yield func(json.RawMessage) error) error {
					return service.Stream(ctx, yield)
				}},
			}
		},
	})
}

var Export = Binding(Key)
var Import = Binding(CallbackKey)

// Function is a Host callback provider. Tests use callbacks as synchronization
// barriers, including validation and cleanup, instead of timing sleeps.
type Function func(context.Context, string) (string, error)

func (f Function) Call(ctx context.Context, input string) (string, error) { return f(ctx, input) }
func (Function) Stream(context.Context, func(json.RawMessage) error) error {
	return errors.New("unused")
}
func (Function) Validate(context.Context) error { return nil }

type Plugin struct {
	WaitValidation bool `json:"wait_validation"`
	FailActivation bool `json:"fail_activation"`
	callback       Service
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "service", New: func() gordis.Plugin { return new(Plugin) }, Requires: []gordis.ServiceSpec{CallbackKey.Spec()}, Provides: []gordis.ServiceSpec{Key.Spec()}}
}
func (p *Plugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	callback, err := scope.Get(CallbackKey)
	if err != nil {
		return err
	}
	p.callback = callback
	if err = scope.Defer("callback", func(ctx context.Context) error { _, err := callback.Call(ctx, "cleanup"); return err }); err != nil {
		return err
	}
	if _, err = callback.Call(ctx, "activate"); err != nil {
		return err
	}
	if p.FailActivation {
		return errors.New("activation failed")
	}
	return scope.Provide(Key, Service(p))
}
func (p *Plugin) Validate(ctx context.Context) error {
	if !p.WaitValidation {
		return nil
	}
	if _, err := p.callback.Call(ctx, "validating"); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
func (p *Plugin) Call(ctx context.Context, input string) (string, error) {
	return p.callback.Call(ctx, input)
}
func (p *Plugin) Stream(ctx context.Context, yield func(json.RawMessage) error) error {
	defer p.callback.Call(context.Background(), "stream-stopped")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(json.RawMessage(`"chunk"`)); err != nil {
			return err
		}
	}
}
func Unit() rpc.Unit {
	config, err := rpc.NewConfig[Config]()
	if err != nil {
		panic(err)
	}
	return rpc.Unit{Plugins: []gordis.Plugin{new(Plugin)}, Instances: []gordis.InstanceSpec{{ID: "service", Plugin: "service"}}, Config: config, Imports: []rpc.Binding{Import}, Exports: []rpc.Export{{Instance: "service", Binding: Export}}}
}
func Identity() rpc.Identity {
	return rpc.Identity{ID: "test/program", Version: "1", Protocol: rpc.Protocol}
}

type Config struct {
	WaitValidation bool `json:"wait_validation,omitempty"`
	FailActivation bool `json:"fail_activation,omitempty"`
}

func (Config) New() Config { return Config{} }
func (c Config) Configure(context.Context) (map[string]any, error) {
	return map[string]any{"service": c}, nil
}
