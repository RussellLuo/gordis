package gordisrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/RussellLuo/gordis"
)

// Export selects a service provided by an instance inside the Unit to expose
// to the Host. The instance ID remains local to the child program.
type Export struct {
	Instance string
	Binding  Binding
}

// Unit describes the child program's local assembly and process boundary.
// Services used only between local instances need no Imports or Exports entry.
type Unit struct {
	Plugins   []gordis.Plugin
	Instances []gordis.InstanceSpec
	Config    ConfigSpec
	// Imports are Host services made available as local proxies to the instances.
	Imports []Binding
	// Exports are local instance services exposed to the Host.
	Exports []Export
}

// Boundary projects the Unit's imports and exports into its external contract,
// omitting internal export source IDs. The Host's ChildBoundary must match it.
func (u Unit) Boundary() Boundary {
	b := Boundary{Imports: append([]Binding(nil), u.Imports...)}
	for _, e := range u.Exports {
		b.Exports = append(b.Exports, e.Binding)
	}
	return b
}
func (u Unit) validate() error {
	if _, e := declarations(u.Boundary()); e != nil {
		return e
	}
	specs := map[string]gordis.PluginSpec{}
	for _, p := range u.Plugins {
		if nilValue(p) {
			return errors.New("nil unit plugin")
		}
		s := p.Spec()
		if s.ID == "rpc/imports" || s.ID == "rpc/exports" {
			return errors.New("reserved plugin ID")
		}
		specs[s.ID] = s
	}
	instances := map[string]gordis.PluginSpec{}
	for _, i := range u.Instances {
		if i.ID == "rpc-imports" || i.ID == "rpc-exports" {
			return errors.New("reserved instance ID")
		}
		s, ok := specs[i.Plugin]
		if !ok {
			return fmt.Errorf("unknown plugin %s", i.Plugin)
		}
		if _, ok = instances[i.ID]; ok {
			return errors.New("duplicate instance ID")
		}
		instances[i.ID] = s
	}
	// A Unit is closed assembly, unlike a dynamic Host that may wait for a
	// future provider. Check membership only; Gordis owns cycle detection,
	// preparation, ordering, activation and rollback.
	provided := map[string]gordis.ServiceSpec{}
	for _, b := range u.Imports {
		provided[b.Spec().Name()] = b.Spec()
	}
	for _, spec := range instances {
		for _, service := range spec.Provides {
			if _, exists := provided[service.Name()]; exists {
				return fmt.Errorf("duplicate provider %s", service.Name())
			}
			provided[service.Name()] = service
		}
	}
	for _, spec := range instances {
		for _, service := range spec.Requires {
			if provided[service.Name()] != service {
				return fmt.Errorf("missing or wrong-type dependency %s", service.Name())
			}
		}
	}
	for _, e := range u.Exports {
		s, ok := instances[e.Instance]
		if !ok {
			return errors.New("unknown export source")
		}
		found := false
		for _, p := range s.Provides {
			found = found || p == e.Binding.Spec()
		}
		if !found {
			return fmt.Errorf("export source %s does not provide %s", e.Instance, e.Binding.Spec().Name())
		}
	}
	return nil
}

type importProvider struct {
	bindings []Binding
	conn     invokerFactory
}
type invokerFactory func(Binding) Invoker

func (p *importProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "rpc/imports", New: func() gordis.Plugin { return &importProvider{p.bindings, p.conn} }, Provides: RequiredServices(p.bindings)}
}
func (p *importProvider) Activate(_ context.Context, s *gordis.Scope) error {
	for _, b := range p.bindings {
		if e := b.provide(s, p.conn(b)); e != nil {
			return e
		}
	}
	return nil
}

type exportProvider struct {
	validationCtx context.Context
	bindings      []Binding
	handlers      map[string]boundHandler
}
type boundHandler struct {
	Handler
	scope *gordis.Scope
}

func (provider *exportProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "rpc/exports", New: func() gordis.Plugin {
		return &exportProvider{validationCtx: provider.validationCtx, bindings: provider.bindings, handlers: provider.handlers}
	}, Requires: RequiredServices(provider.bindings)}
}
func (provider *exportProvider) Activate(ctx context.Context, scope *gordis.Scope) error {
	// Gordis activation has its own lifetime; the export check must also stop
	// when the Initialize request or its startup deadline ends.
	validationCtx, cancel := context.WithCancel(provider.validationCtx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	for _, binding := range provider.bindings {
		handler, err := binding.resolve(validationCtx, scope)
		if err != nil {
			return err
		}
		if len(handler.Methods)+len(handler.Streams) == 0 {
			return errors.New("empty export handler")
		}
		provider.handlers[binding.Spec().Name()] = boundHandler{handler, scope}
	}
	return nil
}
func assemble(ctx context.Context, unit Unit, config json.RawMessage, imports invokerFactory) (*gordis.Host, map[string]boundHandler, error) {
	if err := unit.validate(); err != nil {
		return nil, nil, err
	}
	instances, err := unit.Config.configure(ctx, config, unit.Instances)
	if err != nil {
		return nil, nil, err
	}
	handlers := map[string]boundHandler{}
	plugins := append([]gordis.Plugin(nil), unit.Plugins...)
	plugins = append(plugins, &importProvider{unit.Imports, imports}, &exportProvider{validationCtx: ctx, bindings: unit.Boundary().Exports, handlers: handlers})
	host, err := gordis.NewHost(plugins...)
	if err != nil {
		return nil, nil, err
	}
	changes := host.Changes()
	changes.Mount(host.Env(), gordis.InstanceSpec{ID: "rpc-imports", Plugin: "rpc/imports"})
	for _, instance := range instances {
		changes.Mount(host.Env(), instance)
	}
	operation, err := changes.Apply(ctx)
	if err == nil {
		err = operation.WaitReady(ctx)
	}
	if err == nil {
		_, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "rpc-exports", Plugin: "rpc/exports"})
	}
	return host, handlers, err
}
