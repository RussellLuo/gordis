// Package greeter implements a regular Gordis Plugin. It has no process-specific
// dependencies and can run either locally or through the proc adapter.
package greeter

import (
	"context"
	"errors"

	"github.com/RussellLuo/gordis"
)

const PluginID = "greeter"

type Greeter interface {
	Greet(context.Context, string) (string, error)
}

var Key = gordis.NewKey[Greeter]("example.process.greeter/1")

type Plugin struct {
	Prefix string `json:"prefix"`
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Provides: []gordis.ServiceSpec{Key.Spec()},
		New: func() gordis.Plugin {
			return &Plugin{Prefix: "Hello"}
		},
	}
}

func (p *Plugin) Validate() error {
	if p.Prefix == "" {
		return errors.New("prefix is required")
	}
	return nil
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide(scope, Key, Greeter(service{prefix: p.Prefix}))
}

type service struct {
	prefix string
}

func (s service) Greet(_ context.Context, name string) (string, error) {
	return s.prefix + ", " + name + "!", nil
}

var _ gordis.Plugin = (*Plugin)(nil)
var _ gordis.Validator = (*Plugin)(nil)
var _ Greeter = service{}
