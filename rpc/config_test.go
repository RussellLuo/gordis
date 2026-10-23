package gordisrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
)

var configMethodCalls struct{ new, configure atomic.Int32 }
var errConfigConfigure = errors.New("business mapping failed")

type configWaitKey struct{}
type lifecycleConfig struct {
	Prefix  string         `json:"prefix,omitempty" jsonschemax:"minLength=1"`
	Limit   int            `json:"limit,omitempty"`
	Pointer *int           `json:"pointer,omitempty"`
	Labels  map[string]int `json:"labels,omitempty"`
	Tags    []string       `json:"tags,omitempty"`
	Wide    int64          `json:"wide,omitempty"`
	Mode    string         `json:"mode,omitempty"`
}

func (lifecycleConfig) New() lifecycleConfig {
	configMethodCalls.new.Add(1)
	nine := 9
	return lifecycleConfig{Prefix: "Hello", Limit: 20, Pointer: &nine, Labels: map[string]int{}, Tags: []string{"default"}}
}

func (*lifecycleConfig) Validate() error { panic("unexpected Validate hook") }
func (c lifecycleConfig) Configure(ctx context.Context) (map[string]any, error) {
	configMethodCalls.configure.Add(1)
	switch c.Mode {
	case "configure-error":
		return nil, errConfigConfigure
	case "empty":
		return nil, nil
	case "wait":
		close(ctx.Value(configWaitKey{}).(chan struct{}))
		<-ctx.Done()
		// A successful Configure result must not suppress cancellation.
		return nil, nil
	}
	c.Labels["calls"]++
	return map[string]any{"first": c, "second": struct {
		Count int `json:"count"`
	}{c.Limit}}, nil
}

func configSpecFor[T ConfigDefinition[T]](t *testing.T) ConfigSpec {
	t.Helper()
	spec, err := NewConfig[T]()
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
func resetConfigCalls() {
	configMethodCalls.new.Store(0)
	configMethodCalls.configure.Store(0)
}
func checkConfigCalls(t *testing.T, new, configure int32) {
	t.Helper()
	got := [2]int32{configMethodCalls.new.Load(), configMethodCalls.configure.Load()}
	if got != [2]int32{new, configure} {
		t.Fatalf("New/Configure calls = %v", got)
	}
}
func configInstances() []gordis.InstanceSpec {
	return []gordis.InstanceSpec{
		{ID: "first", Plugin: "first-plugin", Config: json.RawMessage(`{"old":"replace"}`)},
		{ID: "second", Plugin: "second-plugin", Config: json.RawMessage(`{"old":"replace"}`)},
		{ID: "preset", Plugin: "third-plugin", Config: json.RawMessage(`{"keep":true}`)},
	}
}

func TestConfigDeclarationDoesNotExecuteMethods(t *testing.T) {
	resetConfigCalls()
	spec := configSpecFor[lifecycleConfig](t)
	checkConfigCalls(t, 0, 0)
	raw := spec.schemaJSON()
	want := bytes.Clone(raw)
	raw[0] = '!'
	if !bytes.Equal(spec.schemaJSON(), want) {
		t.Fatal("schema bytes are shared")
	}
	if strings.Contains(string(want), `"default"`) || strings.Contains(string(want), "first-plugin") {
		t.Fatal("schema contains defaults or assembly")
	}
}

func TestConfigDefaultsAndStrictDecoding(t *testing.T) {
	spec := configSpecFor[lifecycleConfig](t)
	nine := 9
	for _, tc := range []struct {
		name, raw string
		want      lifecycleConfig
	}{
		{"default", "", lifecycleConfig{Prefix: "Hello", Limit: 20, Pointer: &nine, Tags: []string{"default"}}},
		{"partial-zero", `{"prefix":"Welcome","limit":0,"tags":[]}`, lifecycleConfig{Prefix: "Welcome", Pointer: &nine}},
		{"null", `{"pointer":null,"tags":null}`, lifecycleConfig{Prefix: "Hello", Limit: 20}},
		// The float64 validation copy rounds this integer. Go decoding must use
		// the original JSON instead of re-encoding that copy.
		{"original-number", `{"wide":9007199254740993}`, lifecycleConfig{Prefix: "Hello", Limit: 20, Pointer: &nine, Tags: []string{"default"}, Wide: 9007199254740993}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetConfigCalls()
			out, err := spec.configure(context.Background(), []byte(tc.raw), configInstances())
			if err != nil {
				t.Fatal(err)
			}
			checkConfigCalls(t, 1, 1)
			var got lifecycleConfig
			if err := json.Unmarshal(out[0].Config, &got); err != nil {
				t.Fatal(err)
			}
			tc.want.Labels = map[string]int{"calls": 1}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("config = %+v, want %+v", got, tc.want)
			}
			var mapped struct{ Count *int }
			if err := json.Unmarshal(out[1].Config, &mapped); err != nil || mapped.Count == nil || *mapped.Count != tc.want.Limit {
				t.Fatalf("mapping lost explicit count, including zero: %s, %v", out[1].Config, err)
			}
		})
	}
}

type requiredConfig struct {
	Path string `json:"path"`
}

func (requiredConfig) New() requiredConfig                               { panic("required must be checked before New") }
func (requiredConfig) Configure(context.Context) (map[string]any, error) { panic("unreachable") }

func TestConfigRejectsInputBeforeMethods(t *testing.T) {
	spec := configSpecFor[lifecycleConfig](t)
	for _, raw := range []string{`null`, `[]`, `{`, `{} {}`, `{"unknown":1}`, `{"prefix":""}`} {
		resetConfigCalls()
		if out, err := spec.configure(context.Background(), []byte(raw), configInstances()); err == nil || out != nil {
			t.Fatalf("accepted %s", raw)
		}
		checkConfigCalls(t, 0, 0)
	}
	required := configSpecFor[requiredConfig](t)
	if _, err := required.configure(context.Background(), nil, nil); err == nil {
		t.Fatal("default satisfied required")
	}
}

func TestConfigMethodFailures(t *testing.T) {
	spec := configSpecFor[lifecycleConfig](t)
	for _, tc := range []struct {
		raw   string
		calls [2]int32
		want  error
		stage string
	}{
		{`{"prefix":{"sensitive":"secret-value"}}`, [2]int32{0, 0}, nil, "schema"},
		{`{"wide":98765432109876543210}`, [2]int32{1, 0}, nil, "decode"},
		{`{"mode":"configure-error"}`, [2]int32{1, 1}, errConfigConfigure, "Configure"},
	} {
		resetConfigCalls()
		out, err := spec.configure(context.Background(), []byte(tc.raw), configInstances())
		if err == nil || out != nil || !strings.Contains(err.Error(), tc.stage) {
			t.Fatalf("result = %v, %v", out, err)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Fatalf("lost error: %v", err)
		}
		if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "98765432109876543210") {
			t.Fatalf("error leaks values: %v", err)
		}
		checkConfigCalls(t, tc.calls[0], tc.calls[1])
	}
}

func TestConfigZeroAndEmptyMapping(t *testing.T) {
	var spec ConfigSpec
	if string(spec.schemaJSON()) != emptyConfigSchema {
		t.Fatal("zero schema changed")
	}
	for _, input := range []json.RawMessage{nil, []byte(`{}`)} {
		instances := configInstances()
		out, err := spec.configure(context.Background(), input, instances)
		if err != nil || !reflect.DeepEqual(out, instances) {
			t.Fatalf("zero config: %v %v", out, err)
		}
		out[0].Config[0] = '!'
		if instances[0].Config[0] != '{' {
			t.Fatal("zero config shares presets")
		}
	}
	if _, err := spec.configure(context.Background(), []byte(`{"value":1}`), nil); err == nil {
		t.Fatal("zero config accepted fields")
	}
	declared := configSpecFor[lifecycleConfig](t)
	instances := configInstances()
	out, err := declared.configure(context.Background(), []byte(`{"mode":"empty"}`), instances)
	if err != nil || !reflect.DeepEqual(out, instances) {
		t.Fatalf("empty mapping: %v", err)
	}
}

type mappingMarshaler func() ([]byte, error)

func (m mappingMarshaler) MarshalJSON() ([]byte, error) { return m() }

func TestConfigMapping(t *testing.T) {
	for name, value := range map[string]any{
		"null":   nil,
		"scalar": "secret-value",
		"encode-error": mappingMarshaler(func() ([]byte, error) {
			return nil, errors.New("secret-value")
		}),
	} {
		t.Run(name, func(t *testing.T) {
			instances := configInstances()
			out, err := applyConfigMapping(context.Background(), instances, map[string]any{"first": struct{}{}, "second": value})
			if err == nil || out != nil || !strings.Contains(err.Error(), `"second"`) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("result: %v, %v", out, err)
			}
			if string(instances[0].Config) != `{"old":"replace"}` {
				t.Fatal("partial override")
			}
		})
	}
	var calls atomic.Int32
	_, err := applyConfigMapping(context.Background(), configInstances(), map[string]any{
		"first":   mappingMarshaler(func() ([]byte, error) { calls.Add(1); return []byte(`{}`), nil }),
		"unknown": struct{}{},
	})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("target validation happened after encoding: %v, %d", err, calls.Load())
	}
	raw := json.RawMessage(" \n {\"new\":1} \n ")
	instances := configInstances()
	out, err := applyConfigMapping(context.Background(), instances, map[string]any{"first": raw})
	if err != nil {
		t.Fatal(err)
	}
	raw[0], out[2].Config[0] = '!', '!'
	if string(out[0].Config) != `{"new":1}` || out[0].Plugin != instances[0].Plugin ||
		string(instances[0].Config) != `{"old":"replace"}` || string(instances[2].Config) != `{"keep":true}` {
		t.Fatal("mapping did not replace, preserve metadata, or detach JSON")
	}
}

func TestConfigCancellation(t *testing.T) {
	spec := configSpecFor[lifecycleConfig](t)
	resetConfigCalls()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := spec.configure(ctx, nil, configInstances()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	checkConfigCalls(t, 0, 0)
	ctx, cancel = context.WithCancel(context.Background())
	_, err := applyConfigMapping(ctx, configInstances(), map[string]any{
		"first": mappingMarshaler(func() ([]byte, error) { cancel(); return []byte(`{}`), nil }),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	ctx = context.WithValue(ctx, configWaitKey{}, ready)
	done := make(chan error, 1)
	go func() { _, err := spec.configure(ctx, []byte(`{"mode":"wait"}`), configInstances()); done <- err }()
	<-ready
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	checkConfigCalls(t, 1, 1)
}

func TestConfigConcurrentIsolation(t *testing.T) {
	spec := configSpecFor[lifecycleConfig](t)
	instances := configInstances()
	input := json.RawMessage(`{"limit":0}`)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := spec.configure(context.Background(), input, instances)
			if err != nil {
				t.Error(err)
				return
			}
			var c lifecycleConfig
			if err := json.Unmarshal(out[0].Config, &c); err != nil {
				t.Error(err)
				return
			}
			if c.Labels["calls"] != 1 || c.Limit != 0 {
				t.Errorf("shared defaults: %+v", c)
			}
			out[2].Config[0] = '!'
			spec.schemaJSON()[0] = '!'
		}()
	}
	wg.Wait()
	if string(instances[2].Config) != `{"keep":true}` || string(input) != `{"limit":0}` {
		t.Fatal("shared input or instances were modified")
	}
}

type pointerOnlyConfig struct{}

func (*pointerOnlyConfig) New() *pointerOnlyConfig                           { panic("must not run") }
func (*pointerOnlyConfig) Configure(context.Context) (map[string]any, error) { panic("must not run") }

type invalidFieldConfig struct{ Value func() }

func (invalidFieldConfig) New() invalidFieldConfig                           { panic("must not run") }
func (invalidFieldConfig) Configure(context.Context) (map[string]any, error) { panic("must not run") }

func TestConfigRejectsInvalidDeclarations(t *testing.T) {
	if _, err := NewConfig[*pointerOnlyConfig](); err == nil {
		t.Fatal("pointer root accepted")
	}
	if _, err := NewConfig[invalidFieldConfig](); err == nil || !strings.Contains(err.Error(), "config declaration:") {
		t.Fatalf("lost declaration error: %v", err)
	}
}

type uncheckedDefaults struct {
	Prefix string `json:"prefix,omitempty" jsonschemax:"minLength=1"`
}

func (uncheckedDefaults) New() uncheckedDefaults { return uncheckedDefaults{} }
func (c uncheckedDefaults) Configure(context.Context) (map[string]any, error) {
	return map[string]any{"first": c}, nil
}

func TestConfigDoesNotClaimDefaultsAreSchemaValidated(t *testing.T) {
	spec := configSpecFor[uncheckedDefaults](t)
	out, err := spec.configure(context.Background(), nil, configInstances())
	if err != nil {
		t.Fatal(err)
	}
	if string(out[0].Config) != `{}` {
		t.Fatal(string(out[0].Config))
	}
}

// Exercise the adapter's serialized schema and float64 validation copy together.
func TestConfigSchemaJSONRoundTrip(t *testing.T) {
	type config struct {
		Ratio float64 `json:"ratio" jsonschemax:"enum=[0.1,0.2]"`
	}
	schema, err := newConfigSchema(reflect.TypeFor[config]())
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.validate(json.RawMessage(`{"ratio":0.1}`)); err != nil {
		t.Fatal(err)
	}
	if err := schema.validate(json.RawMessage(`{"ratio":0.3}`)); err == nil {
		t.Fatal("schema validation was bypassed")
	}
}

func TestConfigSchemaLoading(t *testing.T) {
	for _, raw := range []string{`{`, `true`, `{"type":"array"}`, `{"type":["object","null"]}`} {
		if _, err := compileConfigSchema([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid JSON or non-object schema: %s", raw)
		}
	}
	raw := json.RawMessage(`{"type":"object"}`)
	schema, err := compileConfigSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = '!'
	if string(schema.raw) != `{"type":"object"}` {
		t.Fatal("schema shares caller-owned bytes")
	}
}

// Loading policy and redacted errors belong to the RPC adapter, even though
// reference resolution itself belongs to jsonschema-go.
func TestConfigSchemaNoExternalResources(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.Write([]byte(`{"type":"object"}`)) }))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(file, []byte(`{"type":"object"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{server.URL, "file://" + file} {
		raw, _ := json.Marshal(map[string]any{"type": "object", "$ref": ref})
		if _, err := compileConfigSchema(raw); err == nil || strings.Contains(err.Error(), ref) {
			t.Fatalf("external reference accepted or echoed: %v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("network loader was called")
	}
}
