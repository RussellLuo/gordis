// Package plugin owns the common plugin construction and configuration path.
// It is shared by the Host and processbridge so native and process execution
// apply exactly the same validation rules.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/RussellLuo/gordis/internal/lifecycle"
)

// Plugin is both a registration prototype and a fresh runtime object. The
// public gordis package owns the lifecycle hook so this package can share the
// construction/configuration path without depending on the public Scope
// facade.
type Plugin interface {
	Spec() Spec
}

// Spec is immutable plugin-type metadata. New must return a fresh Plugin.
type Spec struct {
	ID       string
	Requires []lifecycle.ServiceSpec
	Provides []lifecycle.ServiceSpec
	New      func() Plugin
}

// Validator optionally validates a decoded plugin configuration.
type Validator interface {
	Validate() error
}

// Inspect reads and validates a registration prototype's static contract.
func Inspect(prototype Plugin) (Spec, error) {
	if nilValue(prototype) {
		return Spec{}, errors.New("nil plugin prototype")
	}
	var spec Spec
	if err := lifecycle.Invoke(func() error {
		spec = prototype.Spec()
		return nil
	}); err != nil {
		return Spec{}, fmt.Errorf("plugin prototype Spec: %w", err)
	}
	if err := validateSpec(spec); err != nil {
		return Spec{}, err
	}
	spec.Requires = append([]lifecycle.ServiceSpec(nil), spec.Requires...)
	spec.Provides = append([]lifecycle.ServiceSpec(nil), spec.Provides...)
	return spec, nil
}

// Prepare creates, decodes and validates one disposable or runtime object.
// Callers decide whether to discard it after preflight or pass it to Activate.
func Prepare(spec Spec, raw json.RawMessage) (Plugin, error) {
	var value Plugin
	if err := lifecycle.Invoke(func() error {
		value = spec.New()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("New: %w", err)
	}
	if nilValue(value) {
		return nil, errors.New("New returned a nil plugin")
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	} else {
		raw = append(json.RawMessage(nil), raw...)
	}
	if err := lifecycle.Invoke(func() error { return decodeConfig(raw, value) }); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	if nilValue(value) {
		return nil, errors.New("configuration decoded to a nil plugin")
	}
	var actual Spec
	if err := lifecycle.Invoke(func() error {
		actual = value.Spec()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("runtime plugin Spec: %w", err)
	}
	if !sameSpec(spec, actual) {
		return nil, errors.New("runtime plugin Spec differs from registered contract")
	}
	if validator, ok := value.(Validator); ok {
		if err := lifecycle.Invoke(validator.Validate); err != nil {
			return nil, fmt.Errorf("Validate: %w", err)
		}
	}
	return value, nil
}

func decodeConfig(raw json.RawMessage, value Plugin) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("configuration must be a JSON object")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	switch err := decoder.Decode(&trailing); err {
	case io.EOF:
		return nil
	case nil:
		return errors.New("configuration must contain exactly one JSON object")
	default:
		return err
	}
}

func validateSpec(spec Spec) error {
	if spec.ID == "" || spec.New == nil {
		return errors.New("plugin needs an ID and New")
	}
	for _, group := range [][]lifecycle.ServiceSpec{spec.Requires, spec.Provides} {
		seen := map[string]bool{}
		for _, service := range group {
			if service.Name() == "" || lifecycle.ServiceType(service) == nil {
				return fmt.Errorf("plugin %s: invalid service key", spec.ID)
			}
			if seen[service.Name()] {
				return fmt.Errorf("plugin %s: duplicate declaration %q", spec.ID, service.Name())
			}
			seen[service.Name()] = true
		}
	}
	return nil
}

func sameSpec(want, actual Spec) bool {
	return want.ID == actual.ID &&
		sameServices(want.Requires, actual.Requires) &&
		sameServices(want.Provides, actual.Provides)
}

func sameServices(a, b []lifecycle.ServiceSpec) bool {
	if len(a) != len(b) {
		return false
	}
	remaining := make(map[string]reflect.Type, len(a))
	for _, service := range a {
		remaining[service.Name()] = lifecycle.ServiceType(service)
	}
	for _, service := range b {
		if remaining[service.Name()] != lifecycle.ServiceType(service) {
			return false
		}
		delete(remaining, service.Name())
	}
	return len(remaining) == 0
}

func nilValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
