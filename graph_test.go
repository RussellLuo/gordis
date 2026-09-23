package gordis_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RussellLuo/gordis"
)

func TestGraphValidation(t *testing.T) {
	other := gordis.NewKey[string]("test.other")
	wrong := gordis.NewKey[string]("test.value")
	noop := func(context.Context, *gordis.Scope) error { return nil }
	provider := definition("provider", noop)
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	consumer := definition("consumer", noop)
	consumer.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	for _, tc := range []struct {
		name    string
		plugins []gordis.Plugin
		specs   []gordis.InstanceSpec
		want    string
	}{
		{"missing", []gordis.Plugin{consumer}, []gordis.InstanceSpec{{ID: "c", Plugin: "consumer"}}, "missing dependency"},
		{
			"duplicate instance",
			[]gordis.Plugin{provider},
			[]gordis.InstanceSpec{{ID: "p", Plugin: "provider"}, {ID: "p", Plugin: "provider"}},
			"duplicate instance",
		},
		{
			"conflict",
			[]gordis.Plugin{provider},
			[]gordis.InstanceSpec{{ID: "a", Plugin: "provider"}, {ID: "b", Plugin: "provider"}},
			"conflict",
		},
		{"unknown", nil, []gordis.InstanceSpec{{ID: "a", Plugin: "absent"}}, "unknown plugin"},
		{
			"invalid JSON",
			[]gordis.Plugin{provider},
			[]gordis.InstanceSpec{{ID: "p", Plugin: "provider", Config: json.RawMessage(`{`)}},
			"decode configuration",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gordis.NewHost(tc.plugins, tc.specs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	t.Run("type", func(t *testing.T) {
		consumer.Requires = []gordis.ServiceSpec{wrong.Spec()}
		_, err := gordis.NewHost(
			[]gordis.Plugin{provider, consumer},
			[]gordis.InstanceSpec{{ID: "p", Plugin: "provider"}, {ID: "c", Plugin: "consumer"}},
		)
		if err == nil || !strings.Contains(err.Error(), "type mismatch") {
			t.Fatal(err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		consumer.Requires = []gordis.ServiceSpec{valueKey.Spec()}
		consumer.Provides = []gordis.ServiceSpec{other.Spec()}
		provider.Requires = []gordis.ServiceSpec{other.Spec()}
		_, err := gordis.NewHost(
			[]gordis.Plugin{provider, consumer},
			[]gordis.InstanceSpec{{ID: "p", Plugin: "provider"}, {ID: "c", Plugin: "consumer"}},
		)
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatal(err)
		}
	})
}

func TestSlotBindingValidation(t *testing.T) {
	noop := func(context.Context, *gordis.Scope) error { return nil }
	provider := definition("provider", noop)
	provider.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	consumer := definition("consumer", noop)
	consumer.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	for _, tc := range []struct {
		name            string
		inputs, outputs map[string]string
		want            string
	}{
		{"undeclared input", map[string]string{"typo": "slot"}, nil, "undeclared service"},
		{"undeclared output", nil, map[string]string{"typo": "slot"}, "undeclared service"},
		{"empty input", map[string]string{valueKey.Name(): ""}, nil, "empty slot"},
		{"empty output", nil, map[string]string{valueKey.Name(): ""}, "empty slot"},
		{"missing slot", map[string]string{valueKey.Name(): "absent"}, nil, "missing dependency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gordis.NewHost([]gordis.Plugin{provider, consumer}, []gordis.InstanceSpec{
				{ID: "p", Plugin: "provider", Outputs: tc.outputs}, {ID: "c", Plugin: "consumer", Inputs: tc.inputs},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		key  gordis.ServiceSpec
		want string
	}{
		{"different contract", gordis.NewKey[*int]("other").Spec(), "contract mismatch"},
		{"different type", gordis.NewKey[string]("other").Spec(), "type mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *consumer
			c := &copy
			c.Requires = []gordis.ServiceSpec{tc.key}
			_, err := gordis.NewHost([]gordis.Plugin{provider, c}, []gordis.InstanceSpec{
				{ID: "p", Plugin: "provider", Outputs: map[string]string{valueKey.Name(): "shared"}},
				{ID: "c", Plugin: "consumer", Inputs: map[string]string{tc.key.Name(): "shared"}},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	_, err := gordis.NewHost([]gordis.Plugin{provider}, []gordis.InstanceSpec{
		{ID: "one", Plugin: "provider", Outputs: map[string]string{valueKey.Name(): "shared"}},
		{ID: "two", Plugin: "provider", Outputs: map[string]string{valueKey.Name(): "shared"}},
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatal(err)
	}
}

func TestOwnershipGraphValidation(t *testing.T) {
	noop := definition("node", func(context.Context, *gordis.Scope) error { return nil })
	for _, tc := range []struct {
		name  string
		specs []gordis.InstanceSpec
		want  string
	}{
		{"missing parent", []gordis.InstanceSpec{{ID: "a", Plugin: "node", Parent: "missing"}}, "unknown parent"},
		{"self", []gordis.InstanceSpec{{ID: "a", Plugin: "node", Parent: "a"}}, "cycle"},
		{
			"ownership cycle",
			[]gordis.InstanceSpec{
				{ID: "a", Plugin: "node", Parent: "b"},
				{ID: "b", Plugin: "node", Parent: "a"},
			},
			"cycle",
		},
		{"optional root", []gordis.InstanceSpec{{ID: "a", Plugin: "node", Optional: true}}, "needs a parent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gordis.NewHost([]gordis.Plugin{noop}, tc.specs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	parentCopy, childCopy := *noop, *noop
	parent, child := &parentCopy, &childCopy
	parent.ID, child.ID = "parent", "child"
	parent.Requires, child.Provides = []gordis.ServiceSpec{valueKey.Spec()}, []gordis.ServiceSpec{valueKey.Spec()}
	_, err := gordis.NewHost([]gordis.Plugin{parent, child}, []gordis.InstanceSpec{
		{ID: "parent", Plugin: "parent"}, {ID: "child", Plugin: "child", Parent: "parent"},
	})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal("combined ownership/dependency cycle accepted", err)
	}
}

func TestOptionalRequiresStillValidated(t *testing.T) {
	parent := definition("parent", func(context.Context, *gordis.Scope) error { return nil })
	optional := definition("optional", func(context.Context, *gordis.Scope) error { return nil })
	optional.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	_, err := gordis.NewHost([]gordis.Plugin{parent, optional}, []gordis.InstanceSpec{
		{ID: "parent", Plugin: "parent"},
		{ID: "optional", Plugin: "optional", Parent: "parent", Optional: true},
	})
	if err == nil || !strings.Contains(err.Error(), "missing dependency") {
		t.Fatal("optional child bypassed dependency validation", err)
	}
}
