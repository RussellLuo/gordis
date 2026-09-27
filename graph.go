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

func serviceAddress(name, label string) string { return name + "\x00" + label }

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
	s.view = s.view.clone()
	required, provided := map[string]string{}, map[string]string{}
	for k, v := range s.requiredLabels {
		required[k] = v
	}
	for k, v := range s.providedLabels {
		provided[k] = v
	}
	s.requiredLabels, s.providedLabels = required, provided
	return s
}

func localID(s InstanceSpec) string {
	if s.localID != "" {
		return s.localID
	}
	return s.ID
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
		if len(spec.Config) == 0 {
			spec.Config = json.RawMessage(`{}`)
		}
		spec.Config = append(json.RawMessage(nil), spec.Config...)
		prepared, prepareErr := internalplugin.Prepare(pluginSpec, spec.Config)
		if prepareErr != nil {
			return nil, fmt.Errorf("gordis: instance %s: configuration: %w", spec.ID, prepareErr)
		}
		if _, ok := prepared.(activator); !ok {
			return nil, fmt.Errorf(
				"gordis: instance %s: plugin %q factory result does not implement Activate",
				spec.ID, pluginSpec.ID,
			)
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
		if spec.requiredLabels, err = resolveLabels(pluginSpec.Requires, spec.requiredLabels, spec.view); err != nil {
			return nil, fmt.Errorf("gordis: instance %s required service labels: %w", spec.ID, err)
		}
		if spec.providedLabels, err = resolveLabels(pluginSpec.Provides, spec.providedLabels, spec.view); err != nil {
			return nil, fmt.Errorf("gordis: instance %s provided service labels: %w", spec.ID, err)
		}
		for _, k := range pluginSpec.Provides {
			label := spec.providedLabels[k.Name()]
			address := serviceAddress(k.Name(), label)
			if old, ok := g.providers[address]; ok {
				return nil, fmt.Errorf("gordis: service %q label %q: providers %s and %s conflict", k.Name(), label, old.id, spec.ID)
			}
			g.providers[address] = provider{spec.ID, k}
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
		if parent := i.spec.parent; parent != "" {
			p, ok := g.nodes[parent]
			if !ok {
				return nil, fmt.Errorf("gordis: instance %s: unknown parent %q", id, parent)
			}
			p.children = append(p.children, id)
		}
		seen := map[string]bool{}
		for _, k := range i.plugin.Requires {
			label := i.spec.requiredLabels[k.Name()]
			p, ok := g.providers[serviceAddress(k.Name(), label)]
			if !ok {
				if dynamic {
					i.bindings = append(i.bindings, BindingSnapshot{Service: k.Name(), Label: label})
					continue
				}
				return nil, fmt.Errorf("gordis: instance %s: missing dependency %q with label %q", id, k.Name(), label)
			}
			if lifecycle.ServiceType(p.service) != lifecycle.ServiceType(k) {
				return nil, fmt.Errorf(
					"gordis: service label %q: type mismatch (%v vs %v)",
					label, lifecycle.ServiceType(p.service), lifecycle.ServiceType(k),
				)
			}
			if p.service.Name() != k.Name() {
				return nil, fmt.Errorf("gordis: service label %q: contract mismatch (%q vs %q)", label, p.service.Name(), k.Name())
			}
			i.bindings = append(i.bindings, BindingSnapshot{Service: k.Name(), Label: label, Provider: p.id})
			if !seen[p.id] {
				i.deps = append(i.deps, p.id)
				seen[p.id] = true
				i.prereqs = append(i.prereqs, p.id)
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
		if parent := g.nodes[id].spec.parent; parent != "" {
			if err := visit(parent, append(path, id)); err != nil {
				return err
			}
		}
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

func resolveLabels(declarations []ServiceSpec, mappings map[string]string, view View) (map[string]string, error) {
	out := make(map[string]string, len(declarations))
	for _, key := range declarations {
		out[key.Name()] = view.label(key.Name())
	}
	for key, label := range mappings {
		if _, ok := out[key]; !ok {
			return nil, fmt.Errorf("binding for undeclared service %q", key)
		}
		if label == "" {
			return nil, fmt.Errorf("empty label for service %q", key)
		}
		out[key] = label
	}
	return out, nil
}

// owned selects a static subtree in combined topological order.
func (g *instanceGraph) owned(id string) []string {
	selected := map[string]bool{id: true}
	var ids []string
	for _, candidate := range g.order {
		if selected[g.nodes[candidate].spec.parent] {
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
		if selected[g.nodes[candidate].spec.parent] {
			selected[candidate] = true
		}
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
