// Package business is an ordinary provider; all RPC code is in assembly.
package business

import (
	"context"
	"errors"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture/contract"
	"sync"
)

type Plugin struct {
	ImportOnly bool
	callback   contract.Callback
	mu         sync.Mutex
	exits      map[string]chan struct{}
}

func (p *Plugin) Spec() gordis.PluginSpec {
	s := gordis.PluginSpec{ID: "streamfixture/business", New: func() gordis.Plugin { return &Plugin{ImportOnly: p.ImportOnly, exits: map[string]chan struct{}{}} }, Requires: []gordis.ServiceSpec{contract.CallbackKey.Spec()}}
	if !p.ImportOnly {
		s.Provides = []gordis.ServiceSpec{contract.GeneratorKey.Spec(), contract.InspectorKey.Spec()}
	}
	return s
}
func (p *Plugin) Activate(ctx context.Context, s *gordis.Scope) error {
	callback, err := s.Get(contract.CallbackKey)
	if err != nil {
		return err
	}
	p.callback = callback
	if err := s.Defer("callback cleanup", func(ctx context.Context) error { _, err := callback.Call(ctx, "cleanup"); return err }); err != nil {
		return err
	}
	if _, err := callback.Call(ctx, "activate"); err != nil {
		return err
	}
	if p.ImportOnly {
		return nil
	}
	if err := s.Provide(contract.GeneratorKey, contract.Generator(p)); err != nil {
		return err
	}
	return s.Provide(contract.InspectorKey, contract.Inspector(p))
}
func (p *Plugin) Run(ctx context.Context, token string, count int, yield func(dto.Value) error) error {
	p.mu.Lock()
	done := make(chan struct{})
	p.exits[token] = done
	p.mu.Unlock()
	defer close(done) // proves the actual business handler reached its return, after every barrier
	if _, err := p.callback.Call(ctx, "run/"+token); err != nil {
		return err
	}
	for i := 0; count < 0 || i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(dto.Value{Count: i, Node: &dto.Node{Name: token}}); err != nil {
			return err
		}
		if token == "cancel" || token == "deadline" || token == "hold" {
			<-ctx.Done()
			if token == "hold" {
				if _, err := p.callback.Call(context.Background(), "handler-cancelled"); err != nil {
					return err
				}
			}
			return ctx.Err()
		}
	}
	if token == "fail" {
		return errors.New("stream business failure")
	}
	return nil
}
func (*Plugin) Nullable(_ context.Context, yield contract.Yield) error {
	if err := yield(nil); err != nil {
		return err
	}
	return yield(&dto.Node{Name: "last"})
}
func (p *Plugin) Wait(ctx context.Context, token string) error {
	p.mu.Lock()
	done := p.exits[token]
	p.mu.Unlock()
	if done == nil {
		return errors.New("unknown stream token")
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
