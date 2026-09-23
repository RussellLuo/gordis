package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// peer owns one duplex RPC session, independently of OS process supervision.
// Incoming request IDs and locally originated request IDs have separate maps.
type peer struct {
	mu              sync.Mutex
	base            message
	bindHello       bool
	next, last      uint64
	pending         map[uint64]chan message
	running         map[uint64]context.CancelFunc
	closing, closed bool
	stale           uint64
	err             error
	limits          Limits
	in              io.ReadCloser
	out             io.WriteCloser
	writes          chan outbound
	ctx             context.Context
	cancel          context.CancelFunc
	onFailure       func(error)
	// accept runs under mu and must never invoke user code or block.
	accept            func(message) (invocation, error)
	once              sync.Once
	workers, handlers sync.WaitGroup
}

type invocation struct {
	call    func(context.Context) (any, error)
	release func()
	control bool
	stop    bool
}

type outbound struct {
	data []byte
	ack  chan error
}

func newPeer(in io.ReadCloser, out io.WriteCloser, limits Limits, base message) *peer {
	ctx, cancel := context.WithCancel(context.Background())
	limits = limits.defaults()
	return &peer{in: in, out: out, limits: limits, base: base,
		ctx: ctx, cancel: cancel, writes: make(chan outbound, limits.Queue),
		pending: map[uint64]chan message{}, running: map[uint64]context.CancelFunc{}}
}

func (p *peer) start() {
	p.workers.Add(2)
	go p.read()
	go p.write()
}

func (p *peer) close(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed, p.closing, p.err = true, true, err
		p.pending = map[uint64]chan message{}
		p.mu.Unlock()
		if p.onFailure != nil {
			p.onFailure(err)
		}
		p.cancel()
		_ = p.in.Close()
		_ = p.out.Close()
	})
}

func (p *peer) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return errors.Join(ErrClosed, p.err)
}

func (p *peer) read() {
	defer p.workers.Done()
	scan := bufio.NewScanner(p.in)
	scan.Buffer(make([]byte, 4096), p.limits.MaxMessage+1)
	for scan.Scan() {
		if len(scan.Bytes()) > p.limits.MaxMessage {
			p.close(errors.New("process: message exceeds limit"))
			return
		}
		var m message
		if err := json.Unmarshal(scan.Bytes(), &m); err != nil {
			p.close(fmt.Errorf("process: malformed message: %w", err))
			return
		}
		if err := p.receive(m); err != nil {
			p.close(err)
			return
		}
	}
	err := scan.Err()
	if err == nil {
		err = io.EOF
	}
	p.close(fmt.Errorf("process: read: %w", err))
}

func (p *peer) receive(m message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	if p.bindHello && p.base.Session == "" {
		if m.Type != "request" || m.Method != "hello" || m.Session == "" || m.Instance == "" || m.Generation == 0 {
			return errors.New("process: hello required")
		}
		p.base = message{Session: m.Session, Instance: m.Instance, Generation: m.Generation}
	}
	if m.Session != p.base.Session || m.Instance != p.base.Instance || m.Generation != p.base.Generation {
		p.stale++
		return nil
	}
	if m.ID == 0 {
		return errors.New("process: zero request ID")
	}
	switch m.Type {
	case "response":
		if ch := p.pending[m.ID]; ch != nil {
			delete(p.pending, m.ID)
			ch <- m
		} else {
			p.stale++
		}
		return nil
	case "cancel":
		if cancel := p.running[m.ID]; cancel != nil {
			cancel()
		}
		return nil
	case "request":
		if m.ID <= p.last {
			return errors.New("process: non-increasing request ID")
		}
		p.last = m.ID
	default:
		return errors.New("process: unexpected message type")
	}
	i, err := p.accept(m)
	if err == nil && !i.control && len(p.running) >= p.limits.MaxPending {
		err = &RPCError{Code: "backpressure", Message: "incoming call limit reached"}
	}
	if err != nil {
		if i.release != nil {
			i.release()
		}
		// Reader never waits for a response write or executes business code.
		return p.respond(m, nil, err, false)
	}
	ctx, cancel := context.WithCancel(p.ctx)
	if m.Deadline != 0 {
		cancel()
		ctx, cancel = context.WithDeadline(p.ctx, time.UnixMilli(m.Deadline))
	}
	p.running[m.ID] = cancel
	p.handlers.Add(1)
	go func() {
		defer p.handlers.Done()
		defer cancel()
		result, err := invokeHandler(ctx, i.call)
		if i.release != nil {
			i.release()
		}
		p.mu.Lock()
		delete(p.running, m.ID)
		p.mu.Unlock()
		if err := p.respond(m, result, err, true); err != nil {
			p.close(err)
		} else if i.stop {
			p.close(ErrClosed)
		}
	}()
	return nil
}

func invokeHandler(ctx context.Context, call func(context.Context) (any, error)) (result any, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("handler panic: %v", v)
		}
	}()
	return call(ctx)
}

func businessCall(h CallHandler, p *peer, m message) invocation {
	return invocation{call: func(ctx context.Context) (any, error) {
		// Business requests canceled before dispatch do not enter user code.
		// Controls still run their bookkeeping so cancellation cannot strand
		// initialization or an already accepted lifecycle operation.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if h == nil {
			return nil, &RPCError{Code: "method", Message: "unknown method"}
		}
		return h(ctx, p, m.Method, m.Params)
	}}
}

func reserved(method string) bool {
	return method == "hello" ||
		method == "initialize" ||
		method == "shutdown" ||
		method == "quiesce" ||
		method == "dispose" ||
		method == "failed"
}

func (p *peer) enqueue(m message, ack chan error) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > p.limits.MaxMessage {
		return errors.New("process: outbound message exceeds limit")
	}
	select {
	case <-p.ctx.Done():
		return ErrClosed
	default:
	}
	select {
	case p.writes <- outbound{append(data, '\n'), ack}:
		return nil
	default:
		return ErrBackpressure
	}
}

func (p *peer) respond(request message, result any, callErr error, wait bool) error {
	m := message{
		Type:       "response",
		Session:    request.Session,
		Instance:   request.Instance,
		Generation: request.Generation,
		ID:         request.ID,
	}
	if callErr != nil {
		if !errors.As(callErr, &m.Error) {
			m.Error = &RPCError{Code: "handler", Message: callErr.Error()}
		}
	} else {
		var err error
		m.Result, err = json.Marshal(result)
		if err != nil {
			return err
		}
	}
	var ack chan error
	if wait {
		ack = make(chan error, 1)
	}
	if err := p.enqueue(m, ack); err != nil {
		return err
	}
	if !wait {
		return nil
	}
	select {
	case err := <-ack:
		return err
	case <-p.ctx.Done():
		return ErrClosed
	}
}

func (p *peer) write() {
	defer p.workers.Done()
	for {
		select {
		case w := <-p.writes:
			n, err := p.out.Write(w.data)
			if err == nil && n != len(w.data) {
				err = io.ErrShortWrite
			}
			if w.ack != nil {
				w.ack <- err
			}
			if err != nil {
				p.close(fmt.Errorf("process: write: %w", err))
				return
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *peer) Call(ctx context.Context, method string, params, result any) error {
	if reserved(method) {
		return errors.New("process: reserved method")
	}
	return p.call(ctx, method, params, result, false)
}

func (p *peer) call(ctx context.Context, method string, params, result any, internal bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed || (p.closing && !internal) || p.base.Session == "" {
		p.mu.Unlock()
		return ErrClosed
	}
	if len(p.pending) >= p.limits.MaxPending && !internal {
		p.mu.Unlock()
		return ErrBackpressure
	}
	p.next++
	m := p.base
	m.Type, m.ID, m.Method, m.Params = "request", p.next, method, raw
	if d, ok := ctx.Deadline(); ok {
		m.Deadline = d.UnixMilli()
	}
	ch := make(chan message, 1)
	p.pending[m.ID] = ch
	// Serialize allocation + enqueue so IDs arrive monotonically under concurrency.
	err = p.enqueue(m, nil)
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, m.ID); p.mu.Unlock() }()
	if err != nil {
		return err
	}
	var response message
	select {
	case response = <-ch:
	case <-ctx.Done():
		m.Type, m.Method, m.Params, m.Deadline = "cancel", "", nil, 0
		if err := p.enqueue(m, nil); err != nil && !errors.Is(err, ErrClosed) {
			p.close(fmt.Errorf("process: cannot deliver cancellation: %w", err))
		}
		return ctx.Err()
	case <-p.ctx.Done():
		select {
		case response = <-ch:
		default:
			return p.failure()
		}
	}
	if response.Error != nil {
		return response.Error
	}
	if result != nil {
		return json.Unmarshal(response.Result, result)
	}
	return nil
}

// stopCalls rejects new business calls and cancels accepted incoming handlers.
// Applications drain their request leases before closing the session.
func (p *peer) stopCalls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closing = true
	for _, cancel := range p.running {
		cancel()
	}
}
