package assembly

import (
	"context"
	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture/business"
)

func Unit(mode string) rpc.Unit {
	boundary := processfixture.Boundary(mode)
	u := rpc.Unit{Plugins: []gordis.Plugin{&business.Plugin{Mode: mode}}, Instances: []gordis.InstanceSpec{{ID: "business", Plugin: "processfixture/business"}}, Imports: boundary.Imports}
	for _, binding := range boundary.Exports {
		u.Exports = append(u.Exports, rpc.Export{Instance: "business", Binding: binding})
	}
	if mode == "single" || mode == "multi" || mode == "duplex" {
		config, err := rpc.NewConfig[Config]()
		if err != nil {
			panic(err)
		}
		u.Config = config
	}
	return u
}

// Config belongs to program assembly, not the ordinary provider.
type Config struct {
	Prefix string `json:"prefix,omitempty"`
}

func (Config) New() Config { return Config{Prefix: "Hello"} }
func (c Config) Configure(context.Context) (map[string]any, error) {
	return map[string]any{"business": map[string]string{"prefix": c.Prefix}}, nil
}
