package events

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
)

type childOwner struct {
	ready              chan struct{}
	confirmed, retired bool
}
type childSubscription struct {
	owner   core.OwnerRef
	binding Binding
	kind    core.RemoteHandlerKind
}
type childPeer struct {
	tokens      tokenTable
	sources     map[string]map[string]gordis.ServiceSpec
	mu          sync.Mutex
	bridge      *Bridge
	host        service
	bus         core.RemoteEndpoint
	owners      map[core.OwnerRef]*childOwner
	subs        map[uint64]childSubscription
	enabled     chan struct{}
	enabledOnce sync.Once
	closed      bool
	failure     error
	failed      chan struct{}
}

func newChildPeer(b *Bridge, host service) *childPeer {
	c := &childPeer{bridge: b, host: host, owners: map[core.OwnerRef]*childOwner{}, subs: map[uint64]childSubscription{}, enabled: make(chan struct{}), failed: make(chan struct{})}
	c.bus = core.NewRemoteBus(c)
	return c
}
func (c *childPeer) fail(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure == nil {
		c.failure = err
		close(c.failed)
	}
}
func (c *childPeer) watch(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.failed:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.failure
	}
}
func (c *childPeer) close() { c.mu.Lock(); c.closed = true; c.mu.Unlock(); c.tokens.revoke(nil) }
func (c *childPeer) RegisterOwner(ctx context.Context, ref core.OwnerRef) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return gordis.ErrClosed
	}
	owner := c.owners[ref]
	if owner == nil {
		owner = &childOwner{ready: make(chan struct{})}
		c.owners[ref] = owner
	}
	retired := owner.retired
	c.mu.Unlock()
	if retired {
		return gordis.ErrClosed
	}
	_, err := call(ctx, c.host, "RegisterOwner", ownerRequest{ref})
	return err
}
func (c *childPeer) OwnerReady(ctx context.Context, ref core.OwnerRef) error {
	_, err := call(ctx, c.host, "OwnerReady", ownerRequest{ref})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owner := c.owners[ref]
	if owner == nil {
		return gordis.ErrStale
	}
	if owner.retired {
		return gordis.ErrClosed
	}
	if !owner.confirmed {
		owner.confirmed = true
		close(owner.ready)
	}
	return nil
}
func (c *childPeer) RetireOwner(ctx context.Context, ref core.OwnerRef) error {
	c.mu.Lock()
	owner := c.owners[ref]
	if owner == nil {
		owner = &childOwner{ready: make(chan struct{})}
		c.owners[ref] = owner
	}
	owner.retired = true
	c.tokens.revoke(&ref)
	for id, sub := range c.subs {
		if sub.owner == ref {
			delete(c.subs, id)
		}
	}
	c.mu.Unlock()
	_, err := call(ctx, c.host, "RetireOwner", ownerRequest{ref})
	if err != nil {
		c.fail(err)
	}
	return err
}
func (c *childPeer) Subscribe(ctx context.Context, msg core.RemoteSubscription) (uint64, error) {
	binding, err := c.bridge.local(msg.Contract, Subscribe)
	if err == nil && !binding.validKind(msg.Kind) {
		err = ErrInvalidRequest
	}
	if err != nil {
		return 0, err
	}
	out, err := call(ctx, c.host, "Subscribe", subscribeRequest{msg.Owner, msg.LocalID, msg.Contract.ID(), msg.Kind, msg.Options})
	if err != nil {
		// A lost response may have allocated a pending host ID. Retiring the
		// Session is the only bounded cleanup when that ID is unknown.
		c.mu.Lock()
		owner := c.owners[msg.Owner]
		retiring := c.closed || owner != nil && owner.retired
		c.mu.Unlock()
		if !retiring {
			c.fail(err)
		}
		return 0, err
	}
	c.mu.Lock()
	c.subs[out.ID] = childSubscription{msg.Owner, binding, msg.Kind}
	c.mu.Unlock()
	return out.ID, nil
}
func (c *childPeer) CommitSubscription(ctx context.Context, id uint64) error {
	_, err := call(ctx, c.host, "CommitSubscription", idRequest{id})
	return err
}
func (c *childPeer) Unsubscribe(ctx context.Context, id uint64) error {
	c.mu.Lock()
	delete(c.subs, id)
	c.mu.Unlock()
	_, err := call(ctx, c.host, "Unsubscribe", idRequest{id})
	if err != nil {
		c.fail(err)
	}
	return err
}
func (c *childPeer) Dispatch(ctx context.Context, msg core.RemoteDispatch) (core.RemoteResult, error) {
	binding, err := c.bridge.local(msg.Contract, Publish)
	if err != nil {
		return core.RemoteResult{}, err
	}
	if !binding.validMode(msg.Mode) || msg.Mode != core.RemoteWaterfall && msg.Base != nil {
		return core.RemoteResult{}, ErrInvalidRequest
	}
	var source *string
	if msg.Source != nil {
		spec, ok := c.sources[msg.Owner.Instance][msg.Source.Name()]
		if !ok || spec != *msg.Source {
			return core.RemoteResult{}, ErrInvalidSource
		}
		name := spec.Name()
		source = &name
	}
	c.mu.Lock()
	owner := c.owners[msg.Owner]
	closed := c.closed || owner != nil && owner.retired
	c.mu.Unlock()
	if closed {
		return core.RemoteResult{}, gordis.ErrClosed
	}
	if owner == nil {
		return core.RemoteResult{}, gordis.ErrStale
	}
	for _, gate := range []<-chan struct{}{owner.ready, c.enabled} {
		select {
		case <-gate:
		case <-ctx.Done():
			return core.RemoteResult{}, ctx.Err()
		}
	}
	c.mu.Lock()
	closed = c.closed || owner.retired
	c.mu.Unlock()
	if closed {
		return core.RemoteResult{}, gordis.ErrClosed
	}
	var token uint64
	if msg.Mode == core.RemoteWaterfall {
		base, err := binding.base(msg.Base)
		if err != nil {
			return core.RemoteResult{}, err
		}
		token, err = c.tokens.add(ctx, msg.Owner, binding, base)
		if err != nil {
			return core.RemoteResult{}, err
		}
		defer c.tokens.finish(token)
	}
	result, err := callResult(ctx, c.host, "Dispatch", lazyRequest(func() (any, error) {
		raw, err := binding.encode(msg.Payload)
		return dispatchRequest{Owner: msg.Owner, Contract: msg.Contract.ID(), Mode: msg.Mode, Payload: raw, Source: source, Token: token}, err
	}), binding, binding.decl.Kind == "hook", msg.Mode == core.RemoteWaterfall)
	return result, err
}
func (c *childPeer) invoke(ctx context.Context, method string, raw json.RawMessage) (reply, error) {
	if err := ctx.Err(); err != nil {
		return reply{}, err
	}
	switch method {
	case "Continue":
		return c.tokens.invoke(ctx, raw)
	case "RevokeSubscription":
		var msg idRequest
		if err := decodeWire(raw, &msg); err != nil {
			return reply{}, err
		}
		if msg.ID == 0 {
			return reply{}, ErrInvalidRequest
		}
		c.mu.Lock()
		delete(c.subs, msg.ID)
		c.mu.Unlock()
		c.bus.RevokeSubscription(msg.ID)
		return reply{}, nil
	case "Enable":
		var msg struct{}
		if err := decodeWire(raw, &msg); err != nil {
			return reply{}, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return reply{}, gordis.ErrClosed
		}
		c.enabledOnce.Do(func() { close(c.enabled) })
		return reply{}, nil
	case "Deliver":
		var msg deliverRequest
		if err := decodeWire(raw, &msg); err != nil {
			return reply{}, err
		}
		if (msg.Kind == core.RemoteMiddleware) != (msg.Token != 0) {
			return reply{}, ErrInvalidRequest
		}
		c.mu.Lock()
		sub, ok := c.subs[msg.ID]
		closed := c.closed
		owner := c.owners[msg.Owner]
		c.mu.Unlock()
		if closed {
			return reply{}, gordis.ErrClosed
		}
		if !ok || sub.owner != msg.Owner {
			return reply{}, gordis.ErrStale
		}
		if owner == nil {
			return reply{}, gordis.ErrStale
		}
		c.mu.Lock()
		retired := owner.retired
		c.mu.Unlock()
		if retired {
			return reply{}, gordis.ErrClosed
		}
		if sub.binding.decl.ID != msg.Contract || sub.kind != msg.Kind {
			return reply{}, ErrInvalidRequest
		}
		payload, err := sub.binding.decode(msg.Payload)
		if err != nil {
			return reply{}, err
		}
		var next func(context.Context, any) (any, error)
		if msg.Token != 0 {
			next = remoteNext(c.host, msg.Token, sub.binding)
		}
		if msg.Token != 0 {
			ctx = context.WithValue(ctx, deliveryContextKey{}, tokenRequest{msg.Token, msg.Contract})
		}
		result, err := c.bus.Deliver(ctx, msg.ID, payload, next)
		return resultReply(sub.binding, result, err)
	}
	return reply{}, ErrInvalidRequest
}
