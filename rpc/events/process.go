package events

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

type ProcessPlugin struct {
	bridge  *Bridge
	options rpc.ProcessOptions
	config  json.RawMessage
	mu      sync.Mutex
	session *rpc.Session
	peer    *hostPeer
}

func reserved(name string) bool {
	return name == hostKey.Name() || name == childKey.Name() || name == core.BusKey.Name()
}
func (b *Bridge) NewProcess(options rpc.ProcessOptions) (*ProcessPlugin, error) {
	if b == nil {
		return nil, ErrInvalidRequest
	}
	for _, group := range [][]rpc.Binding{options.ChildBoundary.Imports, options.ChildBoundary.Exports} {
		for _, binding := range group {
			if reserved(binding.Spec().Name()) {
				return nil, ErrInvalidRequest
			}
		}
	}
	options.ChildBoundary.Imports = append(append([]rpc.Binding(nil), options.ChildBoundary.Imports...), b.host)
	options.ChildBoundary.Exports = append(append([]rpc.Binding(nil), options.ChildBoundary.Exports...), b.child)
	// Use the ordinary constructor's declaration checks. No process is acquired.
	if _, err := rpc.NewProcess(options); err != nil {
		return nil, err
	}
	options.Command.Args = append([]string(nil), options.Command.Args...)
	options.Command.Env = append([]string(nil), options.Command.Env...)
	return &ProcessPlugin{bridge: b, options: options}, nil
}
func (p *ProcessPlugin) Spec() gordis.PluginSpec {
	imports := p.options.ChildBoundary.Imports[:len(p.options.ChildBoundary.Imports)-1]
	requires := append(rpc.RequiredServices(imports), core.BusKey.Spec())
	exports := p.options.ChildBoundary.Exports[:len(p.options.ChildBoundary.Exports)-1]
	return gordis.PluginSpec{ID: p.options.PluginID, Requires: requires, Provides: rpc.RequiredServices(exports), New: func() gordis.Plugin { return &ProcessPlugin{bridge: p.bridge, options: p.options} }}
}
func (p *ProcessPlugin) UnmarshalJSON(raw []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return ErrInvalidRequest
	}
	p.config = bytes.Clone(raw)
	return nil
}
func (p *ProcessPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	peer := newHostPeer(p.bridge, scope)
	peer.sources = map[string]gordis.ServiceSpec{}
	for _, b := range p.options.ChildBoundary.Exports {
		if !reserved(b.Spec().Name()) {
			peer.sources[b.Spec().Name()] = b.Spec()
		}
	}
	p.mu.Lock()
	p.peer = peer
	p.mu.Unlock()
	if err := scope.Defer("rpc events", func(context.Context) error { peer.close(); peer.mu.Lock(); defer peer.mu.Unlock(); return peer.failure }); err != nil {
		return err
	}
	if err := scope.OnStop("rpc events", func(context.Context) error { peer.close(); return nil }); err != nil {
		return err
	}
	if err := scope.Go("rpc events failures", peer.watch); err != nil {
		return err
	}
	local, err := rpc.BindLocalImport(p.bridge.host, service(peer))
	if err != nil {
		return err
	}
	session, err := rpc.Attach(ctx, scope, rpc.AttachOptions{Command: p.options.Command, Identity: p.options.Identity, ChildBoundary: p.options.ChildBoundary, Limits: p.options.Limits, Config: p.config, LocalImports: []rpc.LocalImport{local}, Accept: func(ctx context.Context, session *rpc.Session) error {
		prepared, err := session.PrepareExports()
		if err != nil {
			return err
		}
		endpoint, err := rpc.PreparedClient(prepared, childKey)
		if err != nil {
			return err
		}
		if err = peer.accept(ctx, endpoint); err != nil {
			return err
		}
		return prepared.ProvideServices(scope, p.Spec().Provides...)
	}})
	p.mu.Lock()
	p.session = session
	p.mu.Unlock()
	if err != nil {
		peer.close()
		return err
	}
	return scope.AfterReady("rpc events enable", func(ctx context.Context) error {
		// Dispatch may arrive as soon as the Child receives Enable. The loader
		// has committed Ready before this task can run.
		peer.mu.Lock()
		if peer.closed {
			peer.mu.Unlock()
			return gordis.ErrClosed
		}
		peer.enabled = true
		endpoint := peer.endpoint
		peer.mu.Unlock()
		return bounded(ctx, func(ctx context.Context) error { _, err := call(ctx, endpoint, "Enable", struct{}{}); return err })
	})
}
func (p *ProcessPlugin) Snapshot() rpc.Diagnostics {
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session == nil {
		return rpc.Diagnostics{}
	}
	return session.Snapshot()
}
func (p *ProcessPlugin) EventsSnapshot() Diagnostics {
	p.mu.Lock()
	peer := p.peer
	p.mu.Unlock()
	if peer == nil {
		return Diagnostics{}
	}
	return peer.snapshot()
}
