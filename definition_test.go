package gordis_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/RussellLuo/gordis"
)

type configuredPlugin struct {
	Name  string `json:"name"`
	state *configState
	spec  gordis.PluginSpec
}

type configState struct {
	news    int
	started []*configuredPlugin
	values  []string
}

func (p *configuredPlugin) Spec() gordis.PluginSpec { return p.spec }

func (p *configuredPlugin) Validate() error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

func (p *configuredPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	p.state.started = append(p.state.started, p)
	p.state.values = append(p.state.values, scope.ID()+":"+p.Name)
	return nil
}

func configuredPrototype(state *configState) gordis.Plugin {
	var spec gordis.PluginSpec
	spec = gordis.PluginSpec{ID: "configured"}
	spec.New = func() gordis.Plugin {
		state.news++
		return &configuredPlugin{state: state, spec: spec}
	}
	return &configuredPlugin{state: state, spec: spec}
}

func TestPluginConfigurationSnapshotsAndGenerations(t *testing.T) {
	state := &configState{}
	a := json.RawMessage(`{"name":"one"}`)
	h, err := gordis.NewHost([]gordis.Plugin{configuredPrototype(state)}, []gordis.InstanceSpec{
		{ID: "a", Plugin: "configured", Config: a},
		{ID: "b", Plugin: "configured", Config: json.RawMessage(`{"name":"two"}`)},
	})
	must(t, err)
	a[0] = '['
	if state.news != 2 || len(state.started) != 0 {
		t.Fatalf("preflight: news=%d started=%d", state.news, len(state.started))
	}
	must(t, h.Start(deadline(t)))
	must(t, h.StopInstance(deadline(t), "a"))
	must(t, h.StartInstance(deadline(t), "a"))
	must(t, h.Stop(deadline(t)))
	if !reflect.DeepEqual(state.values, []string{"a:one", "b:two", "a:one"}) || state.news != 5 {
		t.Fatal(state.values, state.news)
	}
	if state.started[0] == state.started[2] || state.started[0].Name != "one" || state.started[1].Name != "two" {
		t.Fatal("runtime objects or configuration were reused")
	}
	if find(h, "a").Plugin != "configured" || find(h, "a").Generation != 2 || find(h, "b").Generation != 1 {
		t.Fatal(h.Snapshot())
	}
	raw, err := json.Marshal(find(h, "a"))
	must(t, err)
	if !strings.Contains(string(raw), `"plugin":"configured"`) || strings.Contains(string(raw), `"definition"`) {
		t.Fatal(string(raw))
	}
}

type customPlugin struct {
	spec      gordis.PluginSpec
	validate  func() error
	start     func(context.Context, *gordis.Scope) error
	unmarshal func([]byte) error
}

type panicSpecPlugin struct{ runtime bool }

func (p *panicSpecPlugin) Spec() gordis.PluginSpec {
	if p.runtime {
		panic("runtime spec panic")
	}
	return gordis.PluginSpec{ID: "panic-runtime", New: func() gordis.Plugin { return &panicSpecPlugin{runtime: true} }}
}

func (*panicSpecPlugin) Start(context.Context, *gordis.Scope) error { return nil }

type prototypeSpecPanic struct{}

func (*prototypeSpecPanic) Spec() gordis.PluginSpec { panic("prototype spec panic") }

func (*prototypeSpecPanic) Start(context.Context, *gordis.Scope) error { return nil }

type valuePlugin struct{}

func (valuePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "value", New: func() gordis.Plugin { return valuePlugin{} }}
}

func (valuePlugin) Start(context.Context, *gordis.Scope) error { return nil }

func (p *customPlugin) Spec() gordis.PluginSpec { return p.spec }

func (p *customPlugin) Validate() error {
	if p.validate != nil {
		return p.validate()
	}
	return nil
}

func (p *customPlugin) Start(ctx context.Context, scope *gordis.Scope) error {
	if p.start != nil {
		return p.start(ctx, scope)
	}
	return nil
}

func (p *customPlugin) UnmarshalJSON(raw []byte) error {
	if p.unmarshal != nil {
		return p.unmarshal(raw)
	}
	return nil
}

func TestPluginPreparationFailures(t *testing.T) {
	var typedNil *customPlugin
	for _, tc := range []struct {
		name string
		new  func(gordis.PluginSpec) gordis.Plugin
		want string
	}{
		{"New panic", func(gordis.PluginSpec) gordis.Plugin { panic("new panic") }, "New: panic: new panic"},
		{"nil plugin", func(gordis.PluginSpec) gordis.Plugin { return nil }, "nil plugin"},
		{"typed nil plugin", func(gordis.PluginSpec) gordis.Plugin { return typedNil }, "nil plugin"},
		{"Validate panic", func(spec gordis.PluginSpec) gordis.Plugin {
			return &customPlugin{spec: spec, validate: func() error { panic("validate panic") }}
		}, "Validate: panic: validate panic"},
		{"Validate error", func(spec gordis.PluginSpec) gordis.Plugin {
			return &customPlugin{spec: spec, validate: func() error { return errors.New("invalid") }}
		}, "Validate: invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var spec gordis.PluginSpec
			spec = gordis.PluginSpec{ID: "plugin"}
			spec.New = func() gordis.Plugin { return tc.new(spec) }
			prototype := &customPlugin{spec: spec}
			_, err := gordis.NewHost([]gordis.Plugin{prototype}, []gordis.InstanceSpec{{ID: "plugin", Plugin: "plugin"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPluginPrototypeFailures(t *testing.T) {
	var typedNil *customPlugin
	missingID := gordis.PluginSpec{New: func() gordis.Plugin { return new(customPlugin) }}
	missingNew := gordis.PluginSpec{ID: "missing-new"}
	for _, tc := range []struct {
		name       string
		prototypes []gordis.Plugin
		want       string
	}{
		{"nil", []gordis.Plugin{nil}, "nil plugin prototype"},
		{"typed nil", []gordis.Plugin{typedNil}, "nil plugin prototype"},
		{"Spec panic", []gordis.Plugin{new(prototypeSpecPanic)}, "prototype Spec: panic: prototype spec panic"},
		{"missing ID", []gordis.Plugin{&customPlugin{spec: missingID}}, "plugin needs an ID and New"},
		{"missing New", []gordis.Plugin{&customPlugin{spec: missingNew}}, "plugin needs an ID and New"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gordis.NewHost(tc.prototypes, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	state := &configState{}
	prototype := configuredPrototype(state)
	_, err := gordis.NewHost([]gordis.Plugin{prototype, prototype}, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate plugin") {
		t.Fatal(err)
	}
}

func TestPluginDecodeAndContractFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config json.RawMessage
		mutate func(*customPlugin)
		want   string
	}{
		{"decode", json.RawMessage(`{`), nil, "decode configuration"},
		{
			"custom decode",
			json.RawMessage(`{}`),
			func(p *customPlugin) {
				p.unmarshal = func([]byte) error { return errors.New("custom decode") }
			},
			"custom decode",
		},
		{
			"contract",
			json.RawMessage(`{}`),
			func(p *customPlugin) { p.spec.ID = "changed" },
			"differs from registered contract",
		},
		{
			"service contract",
			json.RawMessage(`{}`),
			func(p *customPlugin) {
				p.spec.Provides = []gordis.ServiceSpec{valueKey.Spec()}
			},
			"differs from registered contract",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var spec gordis.PluginSpec
			spec = gordis.PluginSpec{ID: "plugin"}
			spec.New = func() gordis.Plugin {
				p := &customPlugin{spec: spec}
				if tc.mutate != nil {
					tc.mutate(p)
				}
				return p
			}
			_, err := gordis.NewHost(
				[]gordis.Plugin{&customPlugin{spec: spec}},
				[]gordis.InstanceSpec{{ID: "plugin", Plugin: "plugin", Config: tc.config}},
			)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestUnsupportedDecodeTargetAndRuntimeSpecPanic(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prototype gordis.Plugin
		id        string
		want      string
	}{
		{"value target", valuePlugin{}, "value", "decode configuration"},
		{"runtime Spec panic", new(panicSpecPlugin), "panic-runtime", "runtime plugin Spec: panic: runtime spec panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gordis.NewHost([]gordis.Plugin{tc.prototype}, []gordis.InstanceSpec{{ID: "instance", Plugin: tc.id}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
