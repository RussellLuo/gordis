package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

const maxTokens = 128

type continueRequest struct {
	Token    uint64          `json:"token"`
	Contract string          `json:"contract"`
	Payload  json.RawMessage `json:"payload"`
}
type tokenRequest struct {
	Token    uint64 `json:"token"`
	Contract string `json:"contract"`
}

type continuation struct {
	subscription          uint64
	binding               Binding
	owner                 core.OwnerRef
	ctx                   context.Context
	cancel                context.CancelFunc
	stop                  func() bool
	fn                    func(context.Context, any) (any, error)
	active, used, running bool
	done                  chan struct{}
}

// Entries stay allocated after expiry while an admitted continuation is still
// running. Expiry closes admission; only actual return releases capacity.
type tokenTable struct {
	mu      sync.Mutex
	next    uint64
	closed  bool
	entries map[uint64]*continuation
}

func (t *tokenTable) add(ctx context.Context, owner core.OwnerRef, b Binding, fn func(context.Context, any) (any, error), subscription ...uint64) (uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, core.ErrNextExpired
	}
	if len(t.entries) >= maxTokens {
		return 0, rpc.ErrBackpressure
	}
	if t.entries == nil {
		t.entries = map[uint64]*continuation{}
	}
	c, cancel := context.WithCancel(ctx)
	t.next++
	entry := &continuation{binding: b, owner: owner, ctx: c, cancel: cancel, fn: fn, active: true, done: make(chan struct{})}
	if len(subscription) > 0 {
		entry.subscription = subscription[0]
	}
	t.entries[t.next] = entry
	id := t.next
	entry.stop = context.AfterFunc(c, func() { t.retire(id) })
	return id, nil
}
func (t *tokenTable) retire(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry := t.entries[id]; entry != nil {
		entry.active = false
		entry.cancel()
		if !entry.running {
			delete(t.entries, id)
		}
	}
}

// finish also drains calls admitted before the enclosing RPC returned. The
// enclosing Bus/publisher lease therefore cannot disappear on cancellation.
func (t *tokenTable) finish(id uint64) {
	t.mu.Lock()
	e := t.entries[id]
	if e == nil {
		t.mu.Unlock()
		return
	}
	e.active = false
	e.stop()
	e.cancel()
	running := e.running
	if !running {
		delete(t.entries, id)
	}
	t.mu.Unlock()
	if running {
		<-e.done
	}
}
func (t *tokenTable) revoke(owner *core.OwnerRef) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if owner == nil {
		t.closed = true
	}
	for id, e := range t.entries {
		if owner != nil && e.owner != *owner {
			continue
		}
		e.active = false
		e.stop()
		e.cancel()
		if !e.running {
			delete(t.entries, id)
		}
	}
}
func (t *tokenTable) snapshot() (total, running int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	total = len(t.entries)
	for _, e := range t.entries {
		if e.running {
			running++
		}
	}
	return
}
func (t *tokenTable) invoke(ctx context.Context, raw json.RawMessage) (reply, error) {
	var msg continueRequest
	if err := decodeWire(raw, &msg); err != nil {
		return reply{}, err
	}
	if msg.Token == 0 || msg.Contract == "" {
		return reply{}, ErrInvalidRequest
	}
	t.mu.Lock()
	e := t.entries[msg.Token]
	if e == nil || !e.active || e.ctx.Err() != nil {
		t.mu.Unlock()
		return reply{}, core.ErrNextExpired
	}
	if e.binding.decl.ID != msg.Contract {
		t.mu.Unlock()
		return reply{}, ErrInvalidRequest
	}
	if e.used {
		t.mu.Unlock()
		return reply{}, core.ErrNextCalled
	}
	// Claim once under the lock, then decode outside it.
	// A codec can block, so it must execute outside the lock and be counted as
	// admitted work until it actually returns.
	e.used = true
	e.running = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		e.running = false
		close(e.done)
		if !e.active {
			delete(t.entries, msg.Token)
		}
		t.mu.Unlock()
	}()
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	defer cancel()
	input, err := e.binding.decode(msg.Payload)
	if err != nil {
		return reply{}, err
	}
	if err := c.Err(); err != nil {
		return reply{}, err
	}
	value, err := callContinuation(e.fn, c, input)
	return resultReply(e.binding, core.RemoteResult{Value: value, Handled: err == nil}, err)
}
func remoteNext(peer service, token uint64, b Binding) func(context.Context, any) (any, error) {
	return func(ctx context.Context, input any) (any, error) {
		out, err := callResult(ctx, peer, "Continue", lazyRequest(func() (any, error) {
			raw, err := b.encode(input)
			return continueRequest{Token: token, Contract: b.decl.ID, Payload: raw}, err
		}), b, true, true)
		return out.Value, err
	}
}

func callContinuation(fn func(context.Context, any) (any, error), ctx context.Context, input any) (value any, err error) {
	defer func() {
		if p := recover(); p != nil {
			value = nil
			err = fmt.Errorf("rpc/events: continuation panic: %v", p)
		}
	}()
	return fn(ctx, input)
}

// Expiration at a normal middleware return does not cancel admitted next work.
func (t *tokenTable) expire(raw json.RawMessage) (reply, error) {
	var msg tokenRequest
	if err := decodeWire(raw, &msg); err != nil {
		return reply{}, err
	}
	if msg.Token == 0 || msg.Contract == "" {
		return reply{}, ErrInvalidRequest
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.entries[msg.Token]; e != nil {
		if e.binding.decl.ID != msg.Contract {
			return reply{}, ErrInvalidRequest
		}
		e.active = false
		if !e.running {
			e.stop()
			e.cancel()
			delete(t.entries, msg.Token)
		}
	}
	return reply{}, nil
}
func (t *tokenTable) revokeSubscription(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for token, e := range t.entries {
		if e.subscription == id {
			e.active = false
			e.stop()
			e.cancel()
			if !e.running {
				delete(t.entries, token)
			}
		}
	}
}

type deliveryContextKey struct{}

func (c *childPeer) MiddlewareReturned(ctx context.Context) error {
	msg, ok := ctx.Value(deliveryContextKey{}).(tokenRequest)
	if !ok {
		return ErrInvalidRequest
	}
	err := bounded(context.Background(), func(ctx context.Context) error { _, err := call(ctx, c.host, "ExpireContinuation", msg); return err })
	if err != nil {
		c.fail(err)
	}
	return err
}
