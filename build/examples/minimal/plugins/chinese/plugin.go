package chinese

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
		ID:  "greet/chinese",
		New: func() gordis.Plugin { return new(Plugin) },
	}
}

func (p *Plugin) Activate(_ context.Context, _ *gordis.Scope) error {
	fmt.Printf("你好，%s！\n", p.Name)
	return nil
}
