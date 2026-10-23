// Package consumer uses the ordinary API without importing RPC or providers.
package consumer

import (
	"context"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture/contract"
)

type Services struct {
	Generator contract.Generator
	Inspector contract.Inspector
}
type Plugin struct{ Services chan Services }

func (p *Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "streamfixture/consumer", New: func() gordis.Plugin { return &Plugin{p.Services} }, Requires: []gordis.ServiceSpec{contract.GeneratorKey.Spec(), contract.InspectorKey.Spec()}}
}
func (p *Plugin) Activate(_ context.Context, s *gordis.Scope) error {
	generator, err := s.Get(contract.GeneratorKey)
	if err != nil {
		return err
	}
	inspector, err := s.Get(contract.InspectorKey)
	if err != nil {
		return err
	}
	p.Services <- Services{generator, inspector}
	return nil
}
