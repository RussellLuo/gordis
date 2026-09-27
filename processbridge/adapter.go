package processbridge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/process"
)

// Adapter declares a fixed contract set, independent of the implementation's
// package name. Resolve validates deployment configuration and returns only the
// business JSON in Options.Config. OnClient is an optional diagnostics sink.
type Adapter struct {
	// ID is the logical Gordis plugin type ID registered with the Host. It
	// identifies the plugin contract, not its execution mode.
	ID                 string
	Requires, Provides []Binding
	// Events explicitly registers every Topic/Hook that may cross this process
	// boundary. Local-only events do not belong here.
	Events   []EventBinding
	Resolve  func(json.RawMessage) (process.Options, error)
	OnClient func(*process.Client)
}

// Plugin returns a registration prototype whose runtime objects use the same
// configuration preparation path as native Gordis plugins.
func (a Adapter) Plugin() (gordis.Plugin, error) {
	requires, err := specs(a.Requires)
	if err != nil {
		return nil, err
	}
	requires, err = eventBusRequirement(requires, a.Events)
	if err != nil {
		return nil, err
	}
	provides, err := specs(a.Provides)
	if err != nil {
		return nil, err
	}
	if a.ID == "" || a.Resolve == nil {
		return nil, errors.New("processbridge: adapter needs ID and resolver")
	}
	a.Requires = append([]Binding(nil), a.Requires...)
	a.Provides = append([]Binding(nil), a.Provides...)
	a.Events = append([]EventBinding(nil), a.Events...)
	var spec gordis.PluginSpec
	spec = gordis.PluginSpec{ID: a.ID, Requires: requires, Provides: provides}
	spec.New = func() gordis.Plugin { return &adapterPlugin{adapter: a, spec: spec} }
	return &adapterPlugin{adapter: a, spec: spec}, nil
}

type adapterPlugin struct {
	adapter Adapter
	spec    gordis.PluginSpec
	raw     json.RawMessage
}

func (p *adapterPlugin) Spec() gordis.PluginSpec { return p.spec }

func (p *adapterPlugin) UnmarshalJSON(raw []byte) error {
	p.raw = append(p.raw[:0], raw...)
	return nil
}

func (p *adapterPlugin) Validate() error {
	_, err := p.adapter.Resolve(append(json.RawMessage(nil), p.raw...))
	return err
}

func (p *adapterPlugin) Activate(ctx context.Context, s *gordis.Scope) error {
	o, err := p.adapter.Resolve(append(json.RawMessage(nil), p.raw...))
	if err != nil {
		return err
	}
	handlers := map[string]process.CallHandler{}
	for _, b := range p.adapter.Requires {
		value, err := b.get(s)
		if err != nil {
			return err
		}
		handlers[b.spec.Name()] = b.handler(value)
	}
	eventHost, err := newEventHost(s, p.adapter.Events, p.adapter.Provides)
	if err != nil {
		return err
	}
	o.Instance, o.Generation = s.ID(), s.Generation()
	o.Identity = identityWithBindings(o.Identity, p.adapter.Requires, p.adapter.Provides, p.adapter.Events)
	// These references remain valid through consumer cleanup, even while the
	// provider Scope is frozen. Never acquire a fresh provider entrance lease.
	o.Handler = bridgeHandler(handlers, eventHost.handle)
	var client *process.Client
	calls := &callGate{}
	// Establish ownership BEFORE acquisition: dependency failure may freeze
	// this Scope while Activate is in flight. Registering afterwards could lose
	// cleanup residuals. Lifecycle owners serialize Activate and Dispose.
	if err := s.Defer("processbridge", func(ctx context.Context) error {
		if client == nil {
			return nil
		}
		calls.drain()
		return client.Close(ctx)
	}); err != nil {
		return err
	}
	if err := s.Defer("processbridge event subscriptions", func(context.Context) error {
		eventHost.close()
		return nil
	}); err != nil {
		return err
	}
	if len(p.adapter.Events) != 0 {
		if err := s.AfterReady("processbridge event ready", eventHost.ready); err != nil {
			return err
		}
	}
	var startErr error
	client, startErr = process.Start(ctx, o)
	if client == nil {
		return startErr
	}
	calls.setPeer(client)
	eventHost.setPeer(calls)
	if p.adapter.OnClient != nil {
		p.adapter.OnClient(client)
	}
	if startErr != nil {
		return startErr
	}
	if err := s.Go("processbridge-watch", client.Watch); err != nil {
		return err
	}
	for _, b := range p.adapter.Provides {
		if err := b.provide(s, boundCaller{calls, b.spec.Name()}); err != nil {
			return err
		}
	}
	return nil
}

// A fixed dependency is usable until its consumer disposes. Host graph ordering
// therefore closes this gate in the provider's final disposer, after consumers
// finish. Public request entrances belong to consumer Scopes. Admission occurs
// BEFORE JSON serialization. Waiting for full calls is a stronger send barrier:
// even a call accepted but not yet queued cannot be overtaken by quiesce.
type callGate struct {
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
	peer   process.Caller
}

func (g *callGate) setPeer(peer process.Caller) {
	g.mu.Lock()
	g.peer = peer
	g.mu.Unlock()
}

func (g *callGate) Call(ctx context.Context, method string, params, result any) error {
	g.mu.Lock()
	if g.closed || g.peer == nil {
		g.mu.Unlock()
		return gordis.ErrClosed
	}
	peer := g.peer
	g.active.Add(1)
	g.mu.Unlock()
	defer g.active.Done()
	return peer.Call(ctx, method, params, result)
}

func (g *callGate) drain() { g.mu.Lock(); g.closed = true; g.mu.Unlock(); g.active.Wait() }
