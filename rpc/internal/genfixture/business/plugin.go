// Package business implements the ordinary provider with no RPC dependency.
package business

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/contract"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
	"strings"
)

type Plugin struct {
	Prefix string `json:"prefix"`
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "genfixture/business", New: func() gordis.Plugin { return &Plugin{Prefix: "Hello"} }, Provides: []gordis.ServiceSpec{contract.Key.Spec()}}
}
func (p *Plugin) Activate(_ context.Context, s *gordis.Scope) error {
	return s.Provide(contract.Key, contract.Service(p))
}
func (*Plugin) Empty(_ context.Context, v struct{}) (struct{}, error) { return v, nil }
func (*Plugin) Ping(context.Context) error                            { return nil }
func (*Plugin) Value(context.Context) (int, error)                    { return 42, nil }
func (p *Plugin) Greet(_ context.Context, name string, count int) (string, error) {
	return strings.Repeat(p.Prefix+", "+name+"!", count), nil
}
func (*Plugin) Echo(_ context.Context, v dto.Value) (dto.Value, error)         { return v, nil }
func (*Plugin) EchoJSON(_ context.Context, v json.RawMessage) (dto.Raw, error) { return v, nil }
func (*Plugin) JSONStream(_ context.Context, v json.RawMessage, yield func(dto.Raw) error) error {
	return yield(v)
}
func (*Plugin) Nullable(_ context.Context, v *dto.Node) (*dto.Node, error) { return v, nil }
func (*Plugin) Wait(ctx context.Context) error                             { <-ctx.Done(); return ctx.Err() }
func (*Plugin) Fail(context.Context) error                                 { return errors.New("fixture business failure") }
