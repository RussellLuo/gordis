package business

import (
	"context"
	"errors"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture/contract"
)

type Plugin struct {
	Mode     string `json:"-"`
	Prefix   string `json:"prefix"`
	callback contract.Text
}

func (p *Plugin) Spec() gordis.PluginSpec {
	spec := gordis.PluginSpec{ID: "processfixture/business", New: func() gordis.Plugin {
		return &Plugin{Mode: p.Mode, Prefix: "Hello"}
	}}
	if p.Mode == "duplex" || p.Mode == "import-only" {
		spec.Requires = []gordis.ServiceSpec{contract.CallbackKey.Spec()}
	}
	if p.Mode == "single" || p.Mode == "multi" || p.Mode == "duplex" {
		spec.Provides = []gordis.ServiceSpec{contract.TextKey.Spec()}
	}
	if p.Mode == "multi" {
		spec.Provides = append(spec.Provides, contract.NumberKey.Spec())
	}
	return spec
}
func (p *Plugin) Validate() error {
	if p.Prefix == "" {
		return errors.New("empty prefix")
	}
	return nil
}
func (p *Plugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	if p.Mode == "duplex" || p.Mode == "import-only" {
		callback, err := scope.Get(contract.CallbackKey)
		if err != nil {
			return err
		}
		p.callback = callback
		if err = scope.Defer("callback", func(ctx context.Context) error {
			_, err := callback.Call(ctx, "cleanup")
			return err
		}); err != nil {
			return err
		}
		if _, err = callback.Call(ctx, "activate"); err != nil {
			return err
		}
	}
	for _, service := range p.Spec().Provides {
		if service == contract.TextKey.Spec() {
			if err := scope.Provide(contract.TextKey, contract.Text(p)); err != nil {
				return err
			}
		} else {
			if err := scope.Provide(contract.NumberKey, contract.Number(p)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (p *Plugin) Call(ctx context.Context, input string) (string, error) {
	// A test barrier keeps the actual child handler alive after cancellation.
	// Cleanup must wait for its Ready lease, not just the cancelled RPC client.
	if input == "hold-handler" && p.callback != nil {
		if _, err := p.callback.Call(ctx, "handler-enter"); err != nil {
			return "", err
		}
		<-ctx.Done()
		if _, err := p.callback.Call(context.Background(), "handler-cancelled"); err != nil {
			return "", err
		}
		_, err := p.callback.Call(context.Background(), "handler-exit")
		return "", errors.Join(ctx.Err(), err)
	}
	if p.callback != nil {
		return p.callback.Call(ctx, input)
	}
	return p.Prefix + "/" + input, nil
}
func (*Plugin) Value(context.Context) (int, error) { return 42, nil }
