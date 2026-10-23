package gordisrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/jsonschema-go/jsonschema"
)

// ConfigDefinition declares defaults and maps program configuration to local
// instance configurations. T must be a named, non-pointer struct. New and
// Configure use value receivers. Configure checks business constraints before
// returning instance configurations; there is no separate Validate hook.
// These methods must be short computations, without acquiring resources. New
// must return independent mutable defaults on every call.
type ConfigDefinition[T any] interface {
	New() T
	Configure(context.Context) (map[string]any, error)
}

// ConfigSpec is an immutable program configuration declaration. Its zero value
// accepts only an empty JSON object and preserves preset instance configuration.
type ConfigSpec struct {
	schema *configSchema
	decode func(context.Context, json.RawMessage) (map[string]any, error)
}

// NewConfig checks T, generates and compiles its schema without invoking author
// methods. Schema validation applies to input, before defaults are constructed;
// required fields must therefore be supplied even if New provides a default.
// Fields are required unless their json tag includes omitempty or omitzero.
// jsonschema tags describe fields; jsonschemax tags declare constraints.
// Numeric schema validation uses float64; strict decoding uses the original JSON.
func NewConfig[T ConfigDefinition[T]]() (ConfigSpec, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct || t.Name() == "" {
		return ConfigSpec{}, errors.New("config declaration: expected a named, non-pointer struct")
	}
	schema, err := newConfigSchema(t)
	if err != nil {
		return ConfigSpec{}, err
	}
	return ConfigSpec{schema: schema, decode: func(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var zero T
		value := zero.New()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			// UnmarshalTypeError.Value may contain the supplied number. Report the
			// field and destination type without copying input into diagnostics.
			var typeError *json.UnmarshalTypeError
			if errors.As(err, &typeError) {
				return nil, fmt.Errorf("program config decode: field %q cannot decode as %s", typeError.Field, typeError.Type)
			}
			return nil, errors.New("program config decode: invalid representation")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mapping, err := value.Configure(ctx)
		if err != nil {
			return nil, fmt.Errorf("program config Configure: %w", err)
		}
		return mapping, nil
	}}, nil
}

const emptyConfigSchema = `{"type":"object","additionalProperties":false}`

// schemaJSON always returns detached bytes, including for the zero declaration.
func (spec ConfigSpec) schemaJSON() json.RawMessage {
	if spec.schema == nil {
		return json.RawMessage(emptyConfigSchema)
	}
	return bytes.Clone(spec.schema.raw)
}

// configure performs no activation. It prepares all overrides before returning
// a detached instance slice, so failures cannot partially change the shared Unit.
func (spec ConfigSpec) configure(ctx context.Context, input json.RawMessage, instances []gordis.InstanceSpec) ([]gordis.InstanceSpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, value, err := normalizeProgramConfig(input)
	if err != nil {
		return nil, err
	}
	var mapping map[string]any
	if spec.schema == nil {
		if len(value) != 0 {
			return nil, errors.New("program config schema: no business fields are declared")
		}
	} else {
		if err := spec.schema.validate(raw); err != nil {
			return nil, err
		}
		mapping, err = spec.decode(ctx, raw)
		if err != nil {
			return nil, err
		}
	}
	return applyConfigMapping(ctx, instances, mapping)
}

func normalizeProgramConfig(input json.RawMessage) (json.RawMessage, map[string]json.RawMessage, error) {
	raw := bytes.Clone(input)
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, nil, errors.New("program config decode: expected one JSON object")
	}
	return raw, object, nil
}

// instances have unique IDs, as checked by Unit.validate.
func applyConfigMapping(ctx context.Context, instances []gordis.InstanceSpec, mapping map[string]any) ([]gordis.InstanceSpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := append([]gordis.InstanceSpec(nil), instances...)
	targets := make(map[string]int, len(out))
	for i := range out {
		targets[out[i].ID] = i
		out[i].Config = bytes.Clone(out[i].Config)
	}
	ids := slices.Sorted(maps.Keys(mapping))
	// Check every target before invoking any custom JSON marshaler in values.
	for _, id := range ids {
		if _, ok := targets[id]; !ok {
			return nil, fmt.Errorf("instance mapping %q: unknown target", id)
		}
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(mapping[id])
		if err != nil {
			return nil, fmt.Errorf("instance mapping %q: cannot encode JSON object", id)
		}
		// Marshal returns compact, valid JSON, including for custom marshalers.
		if raw[0] != '{' {
			return nil, fmt.Errorf("instance mapping %q: expected a JSON object", id)
		}
		out[targets[id]].Config = raw
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// configSchema keeps the serialized schema and its compiled validator together.
type configSchema struct {
	raw      json.RawMessage
	compiled *jsonschema.Resolved
}

func newConfigSchema(t reflect.Type) (*configSchema, error) {
	document, err := jsonschema.ForType(t, nil)
	if err != nil {
		return nil, fmt.Errorf("config declaration: %w", err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("config declaration: cannot encode schema")
	}
	// Compile serialized bytes through the same path used by the Host.
	return compileConfigSchema(raw)
}

func compileConfigSchema(raw json.RawMessage) (*configSchema, error) {
	var document jsonschema.Schema
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, errors.New("program config schema: invalid schema representation")
	}
	if document.Type != "object" {
		return nil, errors.New("program config schema: root must declare type object")
	}
	// Use the library's checks and reference resolver without an external loader.
	compiled, err := document.Resolve(nil)
	if err != nil {
		// Library diagnostics may contain schema or input values.
		return nil, errors.New("program config schema: invalid schema or reference")
	}
	return &configSchema{raw: bytes.Clone(raw), compiled: compiled}, nil
}

// Validation uses float64 numbers; strict Go decoding consumes original bytes.
func (schema *configSchema) validate(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("program config schema: invalid JSON or numeric representation")
	}
	if err := schema.compiled.Validate(value); err != nil {
		return errors.New("program config schema: input validation failed")
	}
	return nil
}
