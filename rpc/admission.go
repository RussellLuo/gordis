package rpc

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/RussellLuo/gordis"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrBackpressure identifies immediate admission rejection and message-size
// exhaustion. The returned error also retains its gRPC ResourceExhausted status.
// It does not prove the whole business operation had no effects: a nested call
// or response-size check can fail after earlier work ran. Retry decisions belong
// to the caller's business policy; RPC never retries automatically.
var ErrBackpressure = errors.New("rpc: backpressure")

// AdmissionClass selects an independent pair of Pending/Handling limits from
// Limits. Each peer uses its local Binding declaration, never a caller-supplied
// wire priority. A class change requires a new Binding version.
// Initialize, Quiesce, Dispose and Watch bypass all three classes.
type AdmissionClass uint8

const (
	// Business admits ordinary service calls, including reverse callbacks and
	// server streams. Unclassified methods use Business. Event adapters also
	// use it for Dispatch/Deliver that can run user handlers.
	Business AdmissionClass = iota
	// Control admits short, bounded management operations such as subscription
	// changes and owner readiness/retirement. These handlers must not run user
	// business code or wait for business work to drain.
	Control
	// Continuation admits next/base execution within an existing invocation.
	// It can run user code and remain active for a long time. A separate budget
	// lets accepted work progress when Business is full without occupying
	// Control capacity. The adapter must validate the invocation/token lifetime;
	// ordinary nested service calls remain Business unless explicitly classified.
	Continuation
)

// AdmissionLimit bounds outstanding outbound calls and actual inbound handlers.
// Each zero field means unlimited; positive fields set explicit limits and
// negative fields are invalid. There is no queue. Unlimited work remains counted
// and must drain during shutdown.
//
// Limits are per Session, per peer and per class, shared by all methods and
// contracts in that class. They count concurrent work, not requests per second.
// Pending and Handling are independent and need not have equal capacities.
// A finite cap rejects excess calls immediately with ErrBackpressure and gRPC
// ResourceExhausted; RPC does not wait, retry or borrow another class's capacity.
type AdmissionLimit struct {
	// Pending limits this peer's outbound calls from before JSON serialization
	// until Call/Stream actually returns, including response decoding and stream
	// yield callbacks. A stream holds one slot for its entire lifetime, not one
	// per message. Cancellation alone cannot release a blocked serializer/yield.
	// Zero means unlimited; a positive value is a cap; negative values are invalid.
	Pending int `json:"pending"`
	// Handling limits actual inbound unary/stream executions on this peer.
	// A slot is held until the handler returns, even if its caller has canceled
	// or returned. Canceled handlers that keep running still consume capacity;
	// a new call can therefore be rejected after the previous caller times out.
	// Zero means unlimited; a positive value is a cap; negative values are invalid.
	Handling int `json:"handling"`
}

// AdmissionSnapshot counts work on this side of one Session. Pending includes
// serialization and stream consumption; Handling ends only at actual handler exit.
// Its AdmissionLimit fields contain observed counts, not configured caps: zero
// here means no active work. This snapshot does not report the other peer's work.
type AdmissionSnapshot struct {
	Business     AdmissionLimit `json:"business"`
	Control      AdmissionLimit `json:"control"`
	Continuation AdmissionLimit `json:"continuation"`
}

type admission struct {
	mu                            sync.Mutex
	limits                        [3]AdmissionLimit
	usage                         [3]AdmissionLimit
	pendingClosed, handlingClosed bool
	work                          sync.WaitGroup
}

func newAdmission(l Limits) *admission {
	return &admission{limits: [3]AdmissionLimit{l.Business, l.Control, l.Continuation}}
}

func (l Limits) validate() error {
	for _, limit := range []AdmissionLimit{l.Business, l.Control, l.Continuation} {
		if limit.Pending < 0 || limit.Handling < 0 {
			return errors.New("rpc: negative admission limit")
		}
	}
	return nil
}
func (a *admission) enter(ctx context.Context, class AdmissionClass, pending bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if (pending && a.pendingClosed) || (!pending && a.handlingClosed) {
		return nil, gordis.ErrClosed
	}
	if class > Continuation {
		return nil, errors.New("rpc: invalid admission class")
	}
	count, limit := &a.usage[class].Handling, a.limits[class].Handling
	if pending {
		count, limit = &a.usage[class].Pending, a.limits[class].Pending
	}
	if limit > 0 && *count >= limit {
		return nil, contextError(status.Error(codes.ResourceExhausted, "rpc admission exhausted"))
	}
	(*count)++
	a.work.Add(1)
	return func() {
		a.mu.Lock()
		(*count)--
		a.mu.Unlock()
		a.work.Done()
	}, nil
}
func (a *admission) closeHandling() { a.mu.Lock(); a.handlingClosed = true; a.mu.Unlock() }
func (a *admission) closePending()  { a.mu.Lock(); a.pendingClosed = true; a.mu.Unlock() }
func (a *admission) close() {
	a.mu.Lock()
	a.pendingClosed, a.handlingClosed = true, true
	a.mu.Unlock()
}
func (a *admission) wait() { a.work.Wait() }
func (a *admission) snapshot() AdmissionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AdmissionSnapshot{a.usage[Business], a.usage[Control], a.usage[Continuation]}
}

func (b Binding) handler(h Handler) (Handler, error) {
	for method, class := range b.classes {
		if h.Methods[method] == nil && h.Streams[method] == nil {
			return Handler{}, fmt.Errorf("rpc: class for unknown method %q", method)
		}
		if h.Streams[method] != nil && class != Business {
			return Handler{}, fmt.Errorf("rpc: stream %q must use Business admission", method)
		}
	}
	return h, nil
}
