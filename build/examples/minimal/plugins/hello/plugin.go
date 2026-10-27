package hello

import (
	"context"
	"fmt"

	"github.com/RussellLuo/gordis"
)

type Plugin struct {
	Name string `json:"name"`
}

func Plugins() []gordis.Plugin { return []gordis.Plugin{new(Plugin)} }

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:  "greet/hello",
		New: func() gordis.Plugin { return new(Plugin) },
	}
}

func (p *Plugin) Activate(_ context.Context, _ *gordis.Scope) error {
	fmt.Printf("Hello, %s!\n", p.Name)
	return nil
}
