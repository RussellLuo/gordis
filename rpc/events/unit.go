package events

import (
	"bytes"
	"context"
	"reflect"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

const adapterPluginID = "gordis/rpc/events/adapter"
const adapterInstanceID = "rpc-events-adapter"

func (b *Bridge) Unit(unit rpc.Unit) (rpc.Unit, error) {
	if b == nil {
		return rpc.Unit{}, ErrInvalidRequest
	}
	for _, plugin := range unit.Plugins {
		if plugin == nil || reflect.ValueOf(plugin).Kind() == reflect.Pointer && reflect.ValueOf(plugin).IsNil() {
			return rpc.Unit{}, ErrInvalidRequest
		}
		spec := plugin.Spec()
		if spec.ID == adapterPluginID {
			return rpc.Unit{}, ErrInvalidRequest
		}
		for _, s := range spec.Provides {
			if reserved(s.Name()) {
				return rpc.Unit{}, ErrInvalidRequest
			}
		}
	}
	for _, i := range unit.Instances {
		if i.ID == adapterInstanceID {
			return rpc.Unit{}, ErrInvalidRequest
		}
	}
	for _, binding := range unit.Imports {
		if reserved(binding.Spec().Name()) {
			return rpc.Unit{}, ErrInvalidRequest
		}
	}
	for _, export := range unit.Exports {
		if reserved(export.Binding.Spec().Name()) {
			return rpc.Unit{}, ErrInvalidRequest
		}
	}
	sources := map[string]map[string]gordis.ServiceSpec{}
	for _, export := range unit.Exports {
		if sources[export.Instance] == nil {
			sources[export.Instance] = map[string]gordis.ServiceSpec{}
		}
		sources[export.Instance][export.Binding.Spec().Name()] = export.Binding.Spec()
	}
	unit.Plugins = append(append([]gordis.Plugin(nil), unit.Plugins...), &childPlugin{bridge: b, sources: sources})
	unit.Instances = append([]gordis.InstanceSpec(nil), unit.Instances...)
	for i := range unit.Instances {
		unit.Instances[i].Config = bytes.Clone(unit.Instances[i].Config)
	}
	unit.Instances = append(unit.Instances, gordis.InstanceSpec{ID: adapterInstanceID, Plugin: adapterPluginID})
	unit.Imports = append(append([]rpc.Binding(nil), unit.Imports...), b.host)
	unit.Exports = append(append([]rpc.Export(nil), unit.Exports...), rpc.Export{Instance: adapterInstanceID, Binding: b.child})
	if err := unit.Validate(); err != nil {
		return rpc.Unit{}, err
	}
	return unit, nil
}

type childPlugin struct {
	bridge  *Bridge
	sources map[string]map[string]gordis.ServiceSpec
}

func (p *childPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: adapterPluginID, Requires: []gordis.ServiceSpec{hostKey.Spec()}, Provides: []gordis.ServiceSpec{core.BusKey.Spec(), childKey.Spec()}, New: func() gordis.Plugin { return &childPlugin{bridge: p.bridge, sources: p.sources} }}
}
func (p *childPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	host, err := scope.Get(hostKey)
	if err != nil {
		return err
	}
	peer := newChildPeer(p.bridge, host)
	peer.sources = p.sources
	if err = scope.Defer("rpc events", func(context.Context) error { peer.close(); peer.mu.Lock(); defer peer.mu.Unlock(); return peer.failure }); err != nil {
		return err
	}
	if err = scope.OnStop("rpc events", func(context.Context) error { peer.close(); return nil }); err != nil {
		return err
	}
	if err = scope.Go("rpc events failures", peer.watch); err != nil {
		return err
	}
	if err = scope.Provide(core.BusKey, core.Bus(peer.bus)); err != nil {
		return err
	}
	return scope.Provide(childKey, service(peer))
}
