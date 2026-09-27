// Package events provides a typed, optional local EventBus service for Gordis.
// Subscriptions and publications are owned by the calling generation's Scope.
package events

import (
	"context"
	"reflect"

	"github.com/RussellLuo/gordis"
)

const targetPrefix = "gordis.events.topic:"

type contractKind uint8

const (
	contractTopic contractKind = iota + 1
	contractHook
)

type contractSpec struct {
	id     string
	name   string
	kind   contractKind
	input  reflect.Type
	output reflect.Type
}

// Contract is a typed event routing target. Only Topic and Hook implement it.
type Contract interface {
	gordis.Target
	ID() string
	eventContract() contractSpec
}

// SameContract reports whether two local Contract values have the same ID,
// kind, and exact Go input/output types. Execution adapters use it to reject a
// Plugin's mismatched local declaration before opening a wire route.
func SameContract(left, right Contract) bool {
	if left == nil || right == nil {
		return false
	}
	return sameContract(left.eventContract(), right.eventContract())
}

// Topic identifies a notification payload by stable ID and exact Go type.
type Topic[T any] struct{ spec contractSpec }

// NewTopic creates a typed notification contract. Empty IDs are rejected when
// the contract is first registered or published.
func NewTopic[T any](id string) Topic[T] {
	return Topic[T]{spec: contractSpec{
		id: id, name: targetPrefix + id, kind: contractTopic,
		input: reflect.TypeOf((*T)(nil)).Elem(),
	}}
}

func (t Topic[T]) Name() string                { return t.spec.name }
func (t Topic[T]) ID() string                  { return t.spec.id }
func (t Topic[T]) eventContract() contractSpec { return t.spec }

// Hook identifies a request/result extension point. Selection handlers and
// waterfall middleware registered for one Hook share its routing address but
// are invoked only by their matching dispatch method.
type Hook[I, O any] struct{ spec contractSpec }

// NewHook creates a typed response contract.
func NewHook[I, O any](id string) Hook[I, O] {
	return Hook[I, O]{spec: contractSpec{
		id: id, name: targetPrefix + id, kind: contractHook,
		input:  reflect.TypeOf((*I)(nil)).Elem(),
		output: reflect.TypeOf((*O)(nil)).Elem(),
	}}
}

func (h Hook[I, O]) Name() string                { return h.spec.name }
func (h Hook[I, O]) ID() string                  { return h.spec.id }
func (h Hook[I, O]) eventContract() contractSpec { return h.spec }

// Handler receives one notification.
type Handler[T any] func(context.Context, T) error

// SelectHandler explicitly reports whether it handled a Hook input.
type SelectHandler[I, O any] func(context.Context, I) (result O, handled bool, err error)

// Next is a waterfall continuation. Each Next value may be called at most once
// and only before its Middleware returns. A call already admitted before that
// return remains part of the dispatch and is drained before dispatch completes.
type Next[I, O any] func(context.Context, I) (O, error)

// Middleware may wrap, transform, or short-circuit a waterfall continuation.
type Middleware[I, O any] func(context.Context, I, Next[I, O]) (O, error)
