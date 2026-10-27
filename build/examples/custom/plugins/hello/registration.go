package hello

import (
	"context"
	"fmt"

	"example.com/gordis-build-custom/app"
	"github.com/RussellLuo/gordis"
)

type Plugin struct {
	Name string `json:"name"`
}

func Registration() app.Registration {
	return app.Registration{Prototype: new(Plugin), DefaultName: "World"}
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "greet/hello", New: func() gordis.Plugin { return new(Plugin) }}
}

func (p *Plugin) Activate(_ context.Context, _ *gordis.Scope) error {
	fmt.Printf("Hello, %s!\n", p.Name)
	return nil
}
