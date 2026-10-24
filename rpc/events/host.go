package events

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

const controlTimeout = 4 * time.Second
const maxSubscriptions = 1024

type ownerState struct {
	ready, retired bool
	ctx            context.Context
	cancel         context.CancelFunc
}
type hostSubscription struct {
	id        uint64
	request   subscribeRequest
	binding   Binding
	committed bool
	remove    func()
}

// Diagnostics describes this event peer's resources, separately from RPC's
// transport, actual handler counters, process reaping and Child cleanup report.
type Diagnostics struct {
	Owners, RetiredOwners      int
	Pending, Committed, Active int
	Dispatching, Delivering    int
	Closed                     bool
	Tokens, Continuing         int
	Failure                    string
}

type hostPeer struct {
	tokens                  tokenTable
	sources                 map[string]gordis.ServiceSpec
	mu                      sync.Mutex
	bridge                  *Bridge
	scope                   *gordis.Scope
	endpoint                service
	owners                  map[core.OwnerRef]*ownerState
	subs                    map[uint64]*hostSubscription
	nextID                  uint64
	changed                 chan struct{}
	closed, enabled         bool
	dispatching, delivering int
	failure                 error
	failed                  chan struct{}
}

func newHostPeer(b *Bridge, scope *gordis.Scope) *hostPeer {
	return &hostPeer{bridge: b, scope: scope, owners: map[core.OwnerRef]*ownerState{}, subs: map[uint64]*hostSubscription{}, changed: make(chan struct{}), failed: make(chan struct{})}
}
func (h *hostPeer) signal() { close(h.changed); h.changed = make(chan struct{}) }
func (h *hostPeer) fail(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failure == nil {
		h.failure = err
		close(h.failed)
	}
}
func (h *hostPeer) watch(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.failed:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.failure
	}
}
func (h *hostPeer) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.tokens.revoke(nil)
	h.closed = true
	h.enabled = false
	for _, owner := range h.owners {
		owner.retired = true
		owner.cancel()
	}
	for id := range h.subs {
		h.removeLocked(id)
	}
	h.signal()
}
func (h *hostPeer) snapshot() Diagnostics {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := Diagnostics{Owners: len(h.owners), Closed: h.closed, Dispatching: h.dispatching, Delivering: h.delivering}
	d.Tokens, d.Continuing = h.tokens.snapshot()
	if h.failure != nil {
		d.Failure = h.failure.Error()
	}
	for _, o := range h.owners {
		if o.retired {
			d.RetiredOwners++
		}
	}
	for _, sub := range h.subs {
		switch {
		case sub.remove != nil:
			d.Active++
		case sub.committed:
			d.Committed++
		default:
			d.Pending++
		}
	}
	return d
}
func validOwner(owner core.OwnerRef) bool { return owner.Instance != "" && owner.Generation != 0 }
func (h *hostPeer) ownerLocked(ref core.OwnerRef) (*ownerState, error) {
	if h.closed {
		return nil, gordis.ErrClosed
	}
	owner := h.owners[ref]
	if owner == nil {
		return nil, gordis.ErrStale
	}
	if owner.retired {
		return nil, gordis.ErrClosed
	}
	return owner, nil
}
func (h *hostPeer) removeLocked(id uint64) {
	if sub := h.subs[id]; sub != nil {
		if sub.remove != nil {
			sub.remove()
		}
		delete(h.subs, id)
	}
}
func (h *hostPeer) activateLocked(sub *hostSubscription) error {
	if sub.remove != nil || !sub.committed {
		return nil
	}
	owner, err := h.ownerLocked(sub.request.Owner)
	if err != nil {
		return err
	}
	if !owner.ready {
		return nil
	}
	opts := sub.request.Options
	// The Host peer claims Once at the Bus-admitted proxy entrance, allowing
	// it to confirm Child removal even if encoding or the Deliver RPC fails.
	opts.Once = false
	var remove func()
	switch sub.request.Kind {
	case core.RemoteNotification:
		remove, err = sub.binding.on(h.scope, opts, func(ctx context.Context, msg any) error { _, err := h.deliver(ctx, sub, msg, nil); return err })
	case core.RemoteSelection:
		remove, err = sub.binding.selectOn(h.scope, opts, func(ctx context.Context, msg any) (core.RemoteResult, error) { return h.deliver(ctx, sub, msg, nil) })
	case core.RemoteMiddleware:
		remove, err = sub.binding.use(h.scope, opts, func(ctx context.Context, msg any, next func(context.Context, any) (any, error)) (any, error) {
			r, err := h.deliver(ctx, sub, msg, next)
			return r.Value, err
		})
	default:
		return ErrInvalidRequest
	}
	if err == nil {
		sub.remove = remove
	}
	return err
}

func (h *hostPeer) invoke(ctx context.Context, method string, raw json.RawMessage) (reply, error) {
	if err := ctx.Err(); err != nil {
		return reply{}, err
	}
	switch method {
	case "ExpireContinuation":
		return h.tokens.expire(raw)
	case "Continue":
		return h.tokens.invoke(ctx, raw)
	case "RegisterOwner", "OwnerReady", "RetireOwner":
		var msg ownerRequest
		if err := decodeWire(raw, &msg); err != nil {
			return reply{}, err
		}
		if !validOwner(msg.Owner) {
			return reply{}, ErrInvalidRequest
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		owner := h.owners[msg.Owner]
		if method == "RetireOwner" {
			if owner == nil {
				c, cancel := context.WithCancel(context.Background())
				owner = &ownerState{ctx: c, cancel: cancel}
				h.owners[msg.Owner] = owner
			}
			owner.retired = true
			h.tokens.revoke(&msg.Owner)
			owner.cancel()
			for id, sub := range h.subs {
				if sub.request.Owner == msg.Owner {
					h.removeLocked(id)
				}
			}
			h.signal()
			return reply{}, nil
		}
		if h.closed || owner != nil && owner.retired {
			return reply{}, gordis.ErrClosed
		}
		if method == "RegisterOwner" {
			if owner == nil {
				c, cancel := context.WithCancel(h.scope.Context())
				h.owners[msg.Owner] = &ownerState{ctx: c, cancel: cancel}
				h.signal()
			}
			return reply{}, nil
		}
		if owner == nil {
			return reply{}, gordis.ErrStale
		}
		owner.ready = true
		ids := make([]uint64, 0, len(h.subs))
		for id := range h.subs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			sub := h.subs[id]
			if sub.request.Owner == msg.Owner {
				if err := h.activateLocked(sub); err != nil {
					owner.ready = false
					for _, s := range h.subs {
						if s.request.Owner == msg.Owner && s.remove != nil {
							s.remove()
							s.remove = nil
						}
					}
					return reply{}, err
				}
			}
		}
		h.signal()
		return reply{}, nil
	case "Subscribe":
		var msg subscribeRequest
		if err := decodeWire(raw, &msg); err != nil {
			return reply{}, err
		}
		if msg.LocalID == 0 {
			return reply{}, ErrInvalidRequest
		}
		binding, err := h.bridge.lookup(msg.Contract, Subscribe)
		if err == nil && !binding.validKind(msg.Kind) {
			err = ErrInvalidRequest
		}
		if err != nil {
			return reply{}, err
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, err = h.ownerLocked(msg.Owner); err != nil {
			return reply{}, err
		}
		if len(h.subs) >= maxSubscriptions {
			return reply{}, rpc.ErrBackpressure
		}
		for _, sub := range h.subs {
			if sub.request.Owner == msg.Owner && sub.request.LocalID == msg.LocalID {
				return reply{}, ErrInvalidRequest
			}
		}
		h.nextID++
		id := h.nextID
		h.subs[id] = &hostSubscription{id: id, request: msg, binding: binding}
		return reply{ID: id}, nil
	case "CommitSubscription", "Unsubscribe":
		var msg idRequest
		if err := decodeWire(raw, &msg); err != nil {
			return reply{}, err
		}
		if msg.ID == 0 {
			return reply{}, ErrInvalidRequest
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if method == "Unsubscribe" {
			h.tokens.revokeSubscription(msg.ID)
			h.removeLocked(msg.ID)
			return reply{}, nil
		}
		sub := h.subs[msg.ID]
		if sub == nil {
			return reply{}, gordis.ErrStale
		}
		if _, err := h.ownerLocked(sub.request.Owner); err != nil {
			return reply{}, err
		}
		sub.committed = true
		return reply{}, h.activateLocked(sub)
	case "Dispatch":
		var msg dispatchRequest
		if err := decodeWire(raw, &msg); err != nil {
			return reply{}, err
		}
		binding, err := h.bridge.lookup(msg.Contract, Publish)
		if err == nil && (!binding.validMode(msg.Mode) || (msg.Mode == core.RemoteWaterfall) != (msg.Token != 0)) {
			err = ErrInvalidRequest
		}
		if err != nil {
			return reply{}, err
		}
		var source *gordis.ServiceSpec
		if msg.Source != nil {
			spec, ok := h.sources[*msg.Source]
			if !ok || !h.scope.HasProvided(spec) {
				return reply{}, ErrInvalidSource
			}
			source = &spec
		}
		payload, err := binding.decode(msg.Payload)
		if err != nil {
			return reply{}, err
		}
		h.mu.Lock()
		owner, err := h.ownerLocked(msg.Owner)
		if err == nil && (!owner.ready || !h.enabled) {
			err = gordis.ErrNotReady
		}
		if err != nil {
			h.mu.Unlock()
			return reply{}, err
		}
		h.dispatching++
		h.mu.Unlock()
		defer func() { h.mu.Lock(); h.dispatching--; h.mu.Unlock() }()
		c, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(owner.ctx, cancel)
		defer stop()
		defer cancel()
		var base any
		if msg.Token != 0 {
			base = binding.makeBase(remoteNext(h.endpoint, msg.Token, binding))
		}
		result, err := core.Bind(h.scope).DispatchRemote(c, core.RemoteDispatch{Contract: binding.contract, Mode: msg.Mode, Payload: payload, Source: source, Base: base})
		return resultReply(binding, result, err)
	}
	return reply{}, ErrInvalidRequest
}

func (h *hostPeer) deliver(ctx context.Context, sub *hostSubscription, msg any, next func(context.Context, any) (any, error)) (result core.RemoteResult, err error) {
	if err := ctx.Err(); err != nil {
		return core.RemoteResult{}, err
	}
	h.mu.Lock()
	owner, err := h.ownerLocked(sub.request.Owner)
	if err == nil && h.subs[sub.id] != sub {
		if sub.request.Options.Once {
			h.mu.Unlock()
			return core.RemoteResult{}, nil
		}
		err = gordis.ErrStale
	}
	if err == nil && h.endpoint == nil {
		err = gordis.ErrNotReady
	}
	if err != nil {
		h.mu.Unlock()
		return core.RemoteResult{}, err
	}
	endpoint := h.endpoint
	if sub.request.Options.Once {
		h.removeLocked(sub.id)
	}
	h.delivering++
	h.mu.Unlock()
	if sub.request.Options.Once {
		defer func() {
			cleanupErr := bounded(context.Background(), func(ctx context.Context) error {
				_, err := call(ctx, endpoint, "RevokeSubscription", idRequest{sub.id})
				return err
			})
			if cleanupErr != nil {
				h.fail(cleanupErr)
				err = errors.Join(err, cleanupErr)
			}
		}()
	}
	defer func() { h.mu.Lock(); h.delivering--; h.mu.Unlock() }()
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(owner.ctx, cancel)
	defer stop()
	defer cancel()
	var token uint64
	if next != nil {
		token, err = h.tokens.add(c, sub.request.Owner, sub.binding, next, sub.id)
		if err != nil {
			return core.RemoteResult{}, err
		}
		defer h.tokens.finish(token)
	}
	result, err = callResult(c, endpoint, "Deliver", lazyRequest(func() (any, error) {
		raw, err := sub.binding.encode(msg)
		return deliverRequest{ID: sub.id, Owner: sub.request.Owner, Contract: sub.request.Contract, Kind: sub.request.Kind, Payload: raw, Token: token}, err
	}), sub.binding, sub.request.Kind != core.RemoteNotification, sub.request.Kind == core.RemoteMiddleware)
	return result, err
}

// accept fixes the initial registered owner set after Unit Ready. It does not
// wait for Enable, which is sent only by the loader's AfterReady task.
func (h *hostPeer) accept(ctx context.Context, endpoint service) error {
	h.mu.Lock()
	h.endpoint = endpoint
	initial := make([]core.OwnerRef, 0, len(h.owners))
	for ref := range h.owners {
		initial = append(initial, ref)
	}
	for {
		ready := true
		for _, ref := range initial {
			o := h.owners[ref]
			if o.retired {
				h.mu.Unlock()
				return gordis.ErrClosed
			}
			if !o.ready {
				ready = false
			}
		}
		if h.closed {
			h.mu.Unlock()
			return gordis.ErrClosed
		}
		if ready {
			h.mu.Unlock()
			return nil
		}
		changed := h.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		h.mu.Lock()
	}
}

func bounded(ctx context.Context, fn func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	return fn(c)
}
