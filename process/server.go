package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

type Handler struct {
	// Initialize may call host services before readiness.
	Initialize func(context.Context, Caller, json.RawMessage) error
	// InitializeSession receives the validated wire identity. Use only one initializer.
	InitializeSession func(context.Context, Caller, Session, json.RawMessage) error
	Call              CallHandler
	// Admit is a framework-only, nonblocking admission hook. It runs in wire order
	// before dispatch, must not call the peer, and returns an idempotent release.
	Admit func(method string) (release func(), err error)
	// Lifecycle hooks run once, in order, independently of a control call's context.
	// Quiesce must close entrances; Dispose must drain and release acquired resources.
	Quiesce func()
	Dispose func() CleanupResult
}

// ReportFailure reports a current-session runtime failure without closing the
// transport: final cleanup may still need host callbacks. It never retries.
func ReportFailure(ctx context.Context, caller Caller, err error) error {
	p, ok := caller.(*peer)
	if !ok || err == nil {
		return errors.New("process: invalid failure notification")
	}
	if reportErr := p.call(ctx, "failed", err.Error(), nil, true); reportErr != nil {
		p.close(reportErr)
		return reportErr
	}
	return nil
}

// Serve owns one session, its pipes and handler lifetimes. Lifecycle cleanup is
// also attempted after a lost connection, when remote callbacks can only fail.
func Serve(
	ctx context.Context,
	in io.ReadCloser,
	out io.WriteCloser,
	identity Identity,
	limits Limits,
	h Handler,
) error {
	p := newPeer(in, out, limits, message{})
	p.bindHello = true
	lifecycle := hasContract(identity, LifecycleContract)
	if lifecycle && (h.Quiesce == nil || h.Dispose == nil) {
		return errors.New("process: lifecycle hooks required")
	}
	stage := "hello"
	initStarted := false
	initialized := make(chan struct{})
	var initOnce, quiesceOnce, disposeOnce sync.Once
	quiesced, disposed := make(chan struct{}), make(chan struct{})
	var cleanup CleanupResult
	quiesce := func() {
		quiesceOnce.Do(func() {
			go func() {
				<-initialized
				// The bridge hooks contain their own panic protection for plugin code.
				if h.Quiesce != nil {
					h.Quiesce()
				}
				close(quiesced)
			}()
		})
	}
	dispose := func() {
		quiesce()
		disposeOnce.Do(func() {
			go func() {
				<-quiesced
				if h.Dispose != nil {
					cleanup = h.Dispose()
				} else {
					cleanup.Complete = true
				}
				close(disposed)
			}()
		})
	}
	p.accept = func(m message) (invocation, error) {
		if p.closing {
			return invocation{}, ErrClosed
		}
		switch m.Method {
		case "hello":
			if stage != "hello" {
				return invocation{}, &RPCError{Code: "protocol", Message: "duplicate hello"}
			}
			var hello struct {
				Protocol string `json:"protocol"`
			}
			if json.Unmarshal(m.Params, &hello) != nil || hello.Protocol != Protocol {
				return invocation{}, &RPCError{Code: "protocol", Message: "incompatible protocol"}
			}
			stage = "initialize"
			return invocation{control: true, call: func(context.Context) (any, error) { return identity, nil }}, nil
		case "initialize":
			if stage != "initialize" {
				return invocation{}, &RPCError{Code: "protocol", Message: "unexpected initialize"}
			}
			stage = "initializing"
			initStarted = true
			return invocation{control: true, call: func(ctx context.Context) (any, error) {
				defer initOnce.Do(func() { close(initialized) })
				_, err := invokeHandler(ctx, func(ctx context.Context) (any, error) {
					if h.InitializeSession != nil {
						return nil, h.InitializeSession(ctx, p, Session{m.Session, m.Instance, m.Generation}, m.Params)
					}
					if h.Initialize != nil {
						return nil, h.Initialize(ctx, p, m.Params)
					}
					return nil, nil
				})
				if err == nil {
					err = ctx.Err()
				}
				p.mu.Lock()
				if stage == "initializing" {
					if err == nil {
						stage = "ready"
					} else {
						stage = "failed"
					}
				}
				p.mu.Unlock()
				return nil, err
			}}, nil
		case "quiesce", "dispose":
			if !lifecycle || stage == "hello" || stage == "initialize" {
				return invocation{}, &RPCError{Code: "phase", Message: "lifecycle command before initialization"}
			}
			if m.Method == "dispose" && stage != "quiescing" && stage != "disposing" {
				return invocation{}, &RPCError{Code: "phase", Message: "dispose requires quiesce"}
			}
			if m.Method == "quiesce" {
				if stage != "disposing" {
					stage = "quiescing"
				}
				quiesce()
				return invocation{control: true, call: func(ctx context.Context) (any, error) {
					select {
					case <-quiesced:
						return nil, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}}, nil
			}
			stage = "disposing"
			dispose()
			return invocation{control: true, call: func(ctx context.Context) (any, error) {
				select {
				case <-disposed:
					return cleanup, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}, nil
		case "shutdown":
			if lifecycle {
				select {
				case <-disposed:
				default:
					return invocation{}, &RPCError{Code: "phase", Message: "shutdown requires dispose"}
				}
			}
			stage = "stopping"
			p.closing = true
			for _, cancel := range p.running {
				cancel()
			}
			return invocation{control: true, stop: true, call: func(context.Context) (any, error) { return nil, nil }}, nil
		default:
			if reserved(m.Method) {
				return invocation{}, &RPCError{Code: "method", Message: "reserved method"}
			}
			if stage != "ready" {
				return invocation{}, &RPCError{Code: "not_ready", Message: "plugin is not ready"}
			}
			i := businessCall(h.Call, p, m)
			if h.Admit != nil {
				var err error
				i.release, err = h.Admit(m.Method)
				if err != nil {
					return i, err
				}
			}
			return i, nil
		}
	}
	p.start()
	select {
	case <-ctx.Done():
		p.close(ctx.Err())
	case <-p.ctx.Done():
	}
	p.workers.Wait()
	// Initialization was never dispatched (e.g. a rejected hello).
	if !initStarted {
		initOnce.Do(func() { close(initialized) })
	}
	if lifecycle {
		dispose()
	}
	p.handlers.Wait()
	if lifecycle {
		<-disposed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == ErrClosed || (stage == "stopping" && errors.Is(p.err, io.EOF)) {
		return nil
	}
	return p.err
}
