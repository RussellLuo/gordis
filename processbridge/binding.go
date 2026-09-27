// Package processbridge runs the same Gordis Plugin locally in a child
// process and adapts its explicitly declared contracts to a Host. Native users
// need only the root gordis package.
package processbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/internal/lifecycle"
	"github.com/RussellLuo/gordis/process"
)

// Binding pairs one typed service key with a handwritten request/response codec.
// A handler receives the fixed value captured at activation, never a live lookup.
type Binding struct {
	spec    gordis.ServiceSpec
	client  func(process.Caller) any
	handler func(any) process.CallHandler
	get     func(*gordis.Scope) (any, error)
	provide func(*gordis.Scope, process.Caller) error
}

func Bind[T any](key gordis.Key[T], client func(process.Caller) T, handler func(T) process.CallHandler) Binding {
	if client == nil || handler == nil {
		return Binding{}
	}
	return Binding{
		spec:    key.Spec(),
		client:  func(c process.Caller) any { return client(c) },
		handler: func(v any) process.CallHandler { return handler(v.(T)) },
		get:     func(s *gordis.Scope) (any, error) { return gordis.Get(s, key) },
		provide: func(s *gordis.Scope, c process.Caller) error { return gordis.Provide(s, key, client(c)) },
	}
}

func specs(bindings []Binding) ([]gordis.ServiceSpec, error) {
	out := make([]gordis.ServiceSpec, 0, len(bindings))
	seen := map[string]bool{}
	for _, b := range bindings {
		if b.spec.Name() == "" || b.client == nil || seen[b.spec.Name()] {
			return nil, errors.New("processbridge: invalid or duplicate binding")
		}
		seen[b.spec.Name()] = true
		out = append(out, b.spec)
	}
	return out, nil
}

func match(declared []gordis.ServiceSpec, bindings []Binding) error {
	actual, err := specs(bindings)
	if err != nil {
		return err
	}
	return matchSpecs(declared, actual)
}

func matchSpecs(declared, actual []gordis.ServiceSpec) error {
	if len(actual) != len(declared) {
		return errors.New("processbridge: binding set differs from plugin contract")
	}
	byName := map[string]gordis.ServiceSpec{}
	for _, s := range actual {
		byName[s.Name()] = s
	}
	for _, s := range declared {
		b, ok := byName[s.Name()]
		if !ok || lifecycle.ServiceType(b) != lifecycle.ServiceType(s) {
			return fmt.Errorf("processbridge: incompatible binding %q", s.Name())
		}
		delete(byName, s.Name())
	}
	return nil
}

type call struct {
	Binding string          `json:"binding"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type boundCaller struct {
	peer    process.Caller
	binding string
}

func (c boundCaller) Call(ctx context.Context, method string, params, result any) error {
	// Keep serialization inside the admitted caller, including custom marshalers.
	return c.peer.Call(ctx, "bridge.call", struct {
		Binding string `json:"binding"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{c.binding, method, params}, result)
}

func router(handlers map[string]process.CallHandler) process.CallHandler {
	return func(ctx context.Context, peer process.Caller, method string, raw json.RawMessage) (any, error) {
		var c call
		if method != "bridge.call" {
			return nil, &process.RPCError{Code: "method", Message: "unknown bridge method"}
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		h := handlers[c.Binding]
		if h == nil {
			return nil, &process.RPCError{Code: "binding", Message: "undeclared capability"}
		}
		return h(ctx, boundCaller{peer, c.Binding}, c.Method, c.Params)
	}
}

func identityWithBindings(
	identity process.Identity,
	requires, provides []Binding,
	events []EventBinding,
) process.Identity {
	identity.Contracts = append([]string(nil), identity.Contracts...)
	bindings := append(append([]Binding(nil), requires...), provides...)
	names := append([]string{process.LifecycleContract}, bindingNames(bindings)...)
	names = append(names, eventContractNames(events)...)
	for _, name := range names {
		found := false
		for _, c := range identity.Contracts {
			if c == name {
				found = true
			}
		}
		if !found {
			identity.Contracts = append(identity.Contracts, name)
		}
	}
	return identity
}

func bindingNames(bindings []Binding) []string {
	names := make([]string, 0, len(bindings))
	for _, b := range bindings {
		names = append(names, b.spec.Name())
	}
	return names
}
