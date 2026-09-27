// Package process supervises trusted stdio plugins. Business contracts remain
// application-owned; this package never transports Go services or Scope objects.
package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Protocol is the first public wire baseline for duplex RPC. Additional features
// such as acknowledged lifecycle control are negotiated through contracts.
const Protocol = "gordis.process/1"

// LifecycleContract opts into quiesce/dispose and runtime failure reporting.
const LifecycleContract = "gordis.lifecycle/1"

type Session struct {
	ID         string
	Instance   string
	Generation uint64
}

// CleanupResult distinguishes business cleanup from OS/pipe reclamation.
// Details is owned by the execution adapter, not interpreted by process.
type CleanupResult struct {
	Complete bool            `json:"complete"`
	Error    string          `json:"error,omitempty"`
	Details  json.RawMessage `json:"details,omitempty"`
}

func hasContract(identity Identity, contract string) bool {
	for _, c := range identity.Contracts {
		if c == contract {
			return true
		}
	}
	return false
}

// Caller invokes the other peer's explicitly exported methods. It is safe for
// concurrent use and remains valid only for this connection/generation.
// A canceled or failed call may already have produced side effects; it is never
// automatically retried. Control methods are reserved for the session owner.
type Caller interface {
	Call(context.Context, string, any, any) error
}

// CallHandler serves an incoming business call. peer can be used for a nested
// call back to its originator. Handlers must honor ctx cancellation and must not
// synchronously close/wait for their own session. Returning an RPCError preserves
// its code; other errors and panics become handler errors.
type CallHandler func(ctx context.Context, peer Caller, method string, params json.RawMessage) (any, error)

var (
	ErrBackpressure = errors.New("process: backpressure")
	ErrClosed       = errors.New("process: connection closed")
)

type Identity struct {
	Protocol  string   `json:"protocol"`
	Package   string   `json:"package"`
	Version   string   `json:"version"`
	Contracts []string `json:"contracts"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("process: %s: %s", e.Code, e.Message) }

type message struct {
	Type       string          `json:"type"`
	Session    string          `json:"session"`
	Instance   string          `json:"instance"`
	Generation uint64          `json:"generation"`
	ID         uint64          `json:"id"`
	Method     string          `json:"method,omitempty"`
	Deadline   int64           `json:"deadline,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      *RPCError       `json:"error,omitempty"`
}

// Limits apply independently at each endpoint; they are not negotiated.
type Limits struct {
	MaxMessage int // Maximum JSON line size in bytes, excluding newline (default 64 KiB).
	MaxPending int // Separate limits for outbound calls and incoming handlers (default 16).
	Queue      int // Shared bounded write queue for requests, responses and cancels (default 16).
}

func (l Limits) defaults() Limits {
	if l.MaxMessage <= 0 {
		l.MaxMessage = 64 * 1024
	}
	if l.MaxPending <= 0 {
		l.MaxPending = 16
	}
	if l.Queue <= 0 {
		l.Queue = 16
	}
	return l
}

func compatible(actual, expected Identity) error {
	if actual.Protocol != Protocol ||
		actual.Protocol != expected.Protocol ||
		actual.Package != expected.Package ||
		actual.Version != expected.Version {
		return errors.New("process: incompatible handshake identity")
	}
	for _, required := range expected.Contracts {
		found := false
		for _, c := range actual.Contracts {
			if c == required {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("process: missing contract %s", required)
		}
	}
	return nil
}
