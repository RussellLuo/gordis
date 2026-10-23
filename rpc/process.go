package gordisrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/RussellLuo/gordis"
)

// ProcessOptions describes one program/Unit. Command, identity and child
// boundary are deployment declarations; they are never read from the
// instance's business JSON configuration.
type ProcessOptions struct {
	PluginID string
	Command  Command
	Identity Identity
	// ChildBoundary declares the expected child imports and exports. Imports
	// are services the child needs from the Host; Exports are services it
	// provides to the Host. It must match the child's Unit.Boundary().
	ChildBoundary Boundary
	Limits        Limits
}

// ProcessPlugin publishes all declared child exports as ordinary Gordis
// services after remote Ready. Create a prototype with NewProcess and register
// it with a Host; Spec.New creates independent instance state.
type ProcessPlugin struct {
	options ProcessOptions
	config  json.RawMessage
	mu      sync.Mutex
	session *Session
}

// NewProcess validates declarations and copies option slices without starting
// the program or constructing business instances. A zero Identity permits
// discovery, with the same semantics as Attach.
func NewProcess(options ProcessOptions) (*ProcessPlugin, error) {
	if strings.TrimSpace(options.PluginID) == "" || strings.TrimSpace(options.Command.Path) == "" {
		return nil, errors.New("process: empty plugin ID or command")
	}
	if options.Identity != (Identity{}) {
		if err := options.Identity.validate(); err != nil {
			return nil, err
		}
	}
	if _, err := declarations(options.ChildBoundary); err != nil {
		return nil, err
	}
	options.Command.Args = append([]string(nil), options.Command.Args...)
	options.Command.Env = append([]string(nil), options.Command.Env...)
	options.ChildBoundary.Imports = append([]Binding(nil), options.ChildBoundary.Imports...)
	options.ChildBoundary.Exports = append([]Binding(nil), options.ChildBoundary.Exports...)
	return &ProcessPlugin{options: options}, nil
}

// Spec declares fixed imports as Requires and every export as Provides.
func (p *ProcessPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       p.options.PluginID,
		Requires: RequiredServices(p.options.ChildBoundary.Imports),
		Provides: RequiredServices(p.options.ChildBoundary.Exports),
		New:      func() gordis.Plugin { return &ProcessPlugin{options: p.options} },
	}
}

// UnmarshalJSON retains an independent copy of the business configuration.
// Gordis supplies {} for an omitted instance configuration.
func (p *ProcessPlugin) UnmarshalJSON(raw []byte) error {
	raw, _, err := normalizeProgramConfig(raw)
	if err != nil {
		return err
	}
	p.config = raw
	return nil
}

// Activate attaches a Scope-owned session, prepares every typed client once,
// then publishes those same clients. Any failure rolls back the activation
// and reaps the process; Gordis discards the Scope's staged services.
func (p *ProcessPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	session, err := Attach(ctx, scope, AttachOptions{
		Command: p.options.Command, Identity: p.options.Identity,
		ChildBoundary: p.options.ChildBoundary, Limits: p.options.Limits, Config: p.config,
		Accept: func(_ context.Context, session *Session) error {
			publish := make([]func(*gordis.Scope) error, 0, len(p.options.ChildBoundary.Exports))
			for _, binding := range p.options.ChildBoundary.Exports {
				provide, err := binding.prepare(session.invoker(binding))
				if err != nil {
					return fmt.Errorf("prepare %s: %w", binding.Spec().Name(), err)
				}
				publish = append(publish, provide)
			}
			for i, provide := range publish {
				if err := provide(scope); err != nil {
					return fmt.Errorf("provide %s: %w", p.options.ChildBoundary.Exports[i].Spec().Name(), err)
				}
			}
			return nil
		},
	})
	p.mu.Lock()
	p.session = session
	p.mu.Unlock()
	return err
}

// Snapshot retains failed-Attach diagnostics without exposing mutable Session.
func (p *ProcessPlugin) Snapshot() Diagnostics {
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session == nil {
		return Diagnostics{}
	}
	return session.Snapshot()
}
