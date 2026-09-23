package gordis

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/RussellLuo/gordis/internal/lifecycle"
	internalplugin "github.com/RussellLuo/gordis/internal/plugin"
)

type provider struct {
	id      string
	service ServiceSpec
}

// instanceGraph is a validated description. It has no running Scopes, services,
// operation history or generation counters.
type instanceGraph struct {
	plugins   map[string]PluginSpec
	nodes     map[string]*graphNode
	providers map[string]provider
	order     []string
}

type graphNode struct {
	spec     InstanceSpec
	plugin   PluginSpec
	deps     []string
	children []string
	prereqs  []string
	bindings []BindingSnapshot
}

func copySpec(s InstanceSpec) InstanceSpec {
	s.Config = append(s.Config[:0:0], s.Config...)
	inputs, outputs := map[string]string{}, map[string]string{}
	for k, v := range s.Inputs {
		inputs[k] = v
	}
	for k, v := range s.Outputs {
		outputs[k] = v
	}
	s.Inputs, s.Outputs = inputs, outputs
	return s
}

// buildGraph only constructs a candidate: it never activates or freezes scopes.
func buildGraph(prototypes []Plugin, specs []InstanceSpec, dynamic bool) (*instanceGraph, error) {
	plugins := map[string]PluginSpec{}
	for _, prototype := range prototypes {
		pluginSpec, err := internalplugin.Inspect(prototype)
		if err != nil {
			return nil, fmt.Errorf("gordis: register plugin: %w", err)
		}
		if _, ok := plugins[pluginSpec.ID]; ok {
			return nil, fmt.Errorf("gordis: duplicate plugin %q", pluginSpec.ID)
		}
		plugins[pluginSpec.ID] = pluginSpec
	}
	g := &instanceGraph{plugins: plugins, nodes: map[string]*graphNode{}, providers: map[string]provider{}}
	types := map[string]ServiceSpec{}
	for _, spec := range specs {
		if spec.ID == "" {
			return nil, errors.New("gordis: empty instance ID")
		}
		if _, ok := g.nodes[spec.ID]; ok {
			return nil, fmt.Errorf("gordis: duplicate instance %q", spec.ID)
		}
		pluginSpec, ok := plugins[spec.Plugin]
		if !ok {
			return nil, fmt.Errorf("gordis: instance %s: unknown plugin %q", spec.ID, spec.Plugin)
		}
		if spec.Optional && spec.Parent == "" {
			return nil, fmt.Errorf("gordis: optional instance %s needs a parent", spec.ID)
		}
		if len(spec.Config) == 0 {
			spec.Config = json.RawMessage(`{}`)
		}
		spec.Config = append(json.RawMessage(nil), spec.Config...)
		if _, err := internalplugin.Prepare(pluginSpec, spec.Config); err != nil {
			return nil, fmt.Errorf("gordis: instance %s: configuration: %w", spec.ID, err)
		}
		for _, group := range [][]ServiceSpec{pluginSpec.Requires, pluginSpec.Provides} {
			seen := map[string]bool{}
			for _, k := range group {
				if k.Name() == "" || lifecycle.ServiceType(k) == nil {
					return nil, fmt.Errorf("gordis: instance %s: invalid service key", spec.ID)
				}
				if seen[k.Name()] {
					return nil, fmt.Errorf("gordis: instance %s: duplicate declaration %q", spec.ID, k.Name())
				}
				seen[k.Name()] = true
				if old, ok := types[k.Name()]; ok && lifecycle.ServiceType(old) != lifecycle.ServiceType(k) {
					return nil, fmt.Errorf(
						"gordis: service %q: type mismatch (%v vs %v)",
						k.Name(), lifecycle.ServiceType(old), lifecycle.ServiceType(k),
					)
				}
				types[k.Name()] = k
			}
		}
		var err error
		if spec.Inputs, err = resolveSlots(pluginSpec.Requires, spec.Inputs); err != nil {
			return nil, fmt.Errorf("gordis: instance %s inputs: %w", spec.ID, err)
		}
		if spec.Outputs, err = resolveSlots(pluginSpec.Provides, spec.Outputs); err != nil {
			return nil, fmt.Errorf("gordis: instance %s outputs: %w", spec.ID, err)
		}
		for _, k := range pluginSpec.Provides {
			slot := spec.Outputs[k.Name()]
			if old, ok := g.providers[slot]; ok {
				return nil, fmt.Errorf("gordis: slot %q: providers %s and %s conflict", slot, old.id, spec.ID)
			}
			g.providers[slot] = provider{spec.ID, k}
		}
		g.nodes[spec.ID] = &graphNode{spec: spec, plugin: pluginSpec}
	}
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		i := g.nodes[id]
		if parent := i.spec.Parent; parent != "" {
			p, ok := g.nodes[parent]
			if !ok {
				return nil, fmt.Errorf("gordis: instance %s: unknown parent %q", id, parent)
			}
			p.children = append(p.children, id)
			i.prereqs = append(i.prereqs, parent)
		}
		seen := map[string]bool{}
		for _, k := range i.plugin.Requires {
			slot := i.spec.Inputs[k.Name()]
			p, ok := g.providers[slot]
			if !ok {
				if dynamic && i.spec.AllowPending {
					i.bindings = append(i.bindings, BindingSnapshot{Service: k.Name(), Slot: slot})
					continue
				}
				return nil, fmt.Errorf("gordis: instance %s: missing dependency %q in slot %q", id, k.Name(), slot)
			}
			if lifecycle.ServiceType(p.service) != lifecycle.ServiceType(k) {
				return nil, fmt.Errorf(
					"gordis: slot %q: type mismatch (%v vs %v)",
					slot, lifecycle.ServiceType(p.service), lifecycle.ServiceType(k),
				)
			}
			if p.service.Name() != k.Name() {
				return nil, fmt.Errorf("gordis: slot %q: contract mismatch (%q vs %q)", slot, p.service.Name(), k.Name())
			}
			i.bindings = append(i.bindings, BindingSnapshot{Service: k.Name(), Slot: slot, Provider: p.id})
			if !seen[p.id] {
				i.deps = append(i.deps, p.id)
				seen[p.id] = true
				if p.id != i.spec.Parent {
					i.prereqs = append(i.prereqs, p.id)
				}
			}
		}
		sort.Strings(i.deps)
		sort.Strings(i.prereqs)
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string, []string) error
	visit = func(id string, path []string) error {
		if visiting[id] {
			return fmt.Errorf("gordis: ownership/dependency cycle: %s", strings.Join(append(path, id), " -> "))
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dep := range g.nodes[id].prereqs {
			if err := visit(dep, append(path, id)); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		g.order = append(g.order, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func resolveSlots(declarations []ServiceSpec, mappings map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(declarations))
	for _, key := range declarations {
		out[key.Name()] = key.Name()
	}
	for key, slot := range mappings {
		if _, ok := out[key]; !ok {
			return nil, fmt.Errorf("binding for undeclared service %q", key)
		}
		if slot == "" {
			return nil, fmt.Errorf("empty slot for service %q", key)
		}
		out[key] = slot
	}
	return out, nil
}

// owned selects a static subtree in combined topological order.
func (g *instanceGraph) owned(id string) []string {
	selected := map[string]bool{id: true}
	var ids []string
	for _, candidate := range g.order {
		if selected[g.nodes[candidate].spec.Parent] {
			selected[candidate] = true
		}
		if selected[candidate] {
			ids = append(ids, candidate)
		}
	}
	return ids
}

// affected follows both ownership and service edges to a fixed point. Because
// the combined graph is acyclic and sorted, a single forward pass suffices.
func (g *instanceGraph) affected(id string) []string {
	selected := map[string]bool{id: true}
	var ids []string
	for _, candidate := range g.order {
		for _, dep := range g.nodes[candidate].prereqs {
			if selected[dep] {
				selected[candidate] = true
			}
		}
		if selected[candidate] {
			ids = append(ids, candidate)
		}
	}
	return ids
}
