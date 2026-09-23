package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func lifecyclePeers(t *testing.T, h Handler) (*peer, <-chan error) {
	t.Helper()
	in, out := io.Pipe()
	hostIn, pluginOut := io.Pipe()
	identity := Identity{Protocol: Protocol, Package: "lifecycle", Version: "3", Contracts: []string{LifecycleContract}}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, pluginOut, identity, Limits{}, h) }()
	p := newPeer(hostIn, out, Limits{}, message{Session: "test", Instance: "assigned", Generation: 57})
	p.accept = func(m message) (invocation, error) { return businessCall(nil, p, m), nil }
	p.start()
	t.Cleanup(func() { p.close(ErrClosed); p.workers.Wait(); p.handlers.Wait(); await(t, done) })
	if err := p.call(context.Background(), "hello", map[string]string{"protocol": Protocol}, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := p.call(context.Background(), "initialize", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	return p, done
}

func TestLifecycleCommandsAreOrderedIdempotentAndSurviveCanceledWait(t *testing.T) {
	blocked, release := make(chan struct{}), make(chan struct{})
	var quiesces, disposes atomic.Int32
	p, _ := lifecyclePeers(t, Handler{
		InitializeSession: func(_ context.Context, _ Caller, s Session, _ json.RawMessage) error {
			if s.Instance != "assigned" || s.Generation != 57 {
				t.Error(s)
			}
			return nil
		},
		Quiesce: func() { quiesces.Add(1); close(blocked); <-release },
		Dispose: func() CleanupResult {
			disposes.Add(1)
			return CleanupResult{Complete: true, Error: "persistent cleanup error"}
		},
	})
	var rpc *RPCError
	if err := p.call(context.Background(), "dispose", nil, nil, true); !errors.As(err, &rpc) || rpc.Code != "phase" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- p.call(ctx, "quiesce", nil, nil, true) }()
	await(t, blocked)
	cancel()
	if err := await(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	for n := 0; n < 2; n++ {
		if err := p.call(context.Background(), "quiesce", nil, nil, true); err != nil {
			t.Fatal(err)
		}
		var got CleanupResult
		err := p.call(context.Background(), "dispose", nil, &got, true)
		if err != nil || !got.Complete || got.Error != "persistent cleanup error" {
			t.Fatal(got, err)
		}
	}
	if quiesces.Load() != 1 || disposes.Load() != 1 {
		t.Fatal(quiesces.Load(), disposes.Load())
	}
	if err := p.call(context.Background(), "shutdown", nil, nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestReceiveAdmissionPrecedesQuiesceAndHandlerDispatch(t *testing.T) {
	admitted, release := make(chan struct{}), make(chan struct{})
	var work sync.WaitGroup
	var active atomic.Int32
	p, _ := lifecyclePeers(t, Handler{
		Admit: func(string) (func(), error) {
			work.Add(1)
			active.Add(1)
			close(admitted)
			return func() { active.Add(-1); work.Done() }, nil
		},
		Call: func(context.Context, Caller, string, json.RawMessage) (any, error) { <-release; return "accepted", nil },
		Quiesce: func() {
			if active.Load() != 1 {
				t.Error("request not registered before quiesce")
			}
		},
		Dispose: func() CleanupResult { work.Wait(); return CleanupResult{Complete: true} },
	})
	callResult := make(chan error, 1)
	go func() { callResult <- p.Call(context.Background(), "business", nil, nil) }()
	await(t, admitted)
	if err := p.call(context.Background(), "quiesce", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := p.Call(context.Background(), "business", nil, nil); err == nil {
		t.Fatal("accepted after quiesce")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := p.call(ctx, "dispose", nil, nil, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	if err := await(t, callResult); err != nil {
		t.Fatal(err)
	}
	if err := p.call(context.Background(), "dispose", nil, nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestMismatchedProtocolHelloRejectedBeforeInitialize(t *testing.T) {
	in, out := io.Pipe()
	hostIn, pluginOut := io.Pipe()
	var initialized atomic.Bool
	done := make(chan error, 1)
	go func() {
		handler := Handler{Initialize: func(context.Context, Caller, json.RawMessage) error {
			initialized.Store(true)
			return nil
		}}
		done <- Serve(
			context.Background(), in, pluginOut,
			Identity{Protocol: Protocol}, Limits{}, handler,
		)
	}()
	p := newPeer(hostIn, out, Limits{}, message{Session: "test", Instance: "assigned", Generation: 2})
	p.accept = func(message) (invocation, error) { return invocation{}, errors.New("unexpected request") }
	p.start()
	for _, version := range []string{"gordis.process/0", "other.process/1"} {
		var rpc *RPCError
		err := p.call(context.Background(), "hello", map[string]string{"protocol": version}, nil, true)
		if !errors.As(err, &rpc) || rpc.Code != "protocol" {
			t.Fatal(err)
		}
	}
	if initialized.Load() {
		t.Fatal("mismatched protocol reached initialization")
	}
	p.close(ErrClosed)
	p.workers.Wait()
	p.handlers.Wait()
	await(t, done)
}

func TestOldGenerationFailureNotificationDiscardedBeforeDispatch(t *testing.T) {
	notifications := make(chan struct{}, 1)
	left, right := connectedPeers(t, Limits{}, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		notifications <- struct{}{}
		return nil, nil
	}, nil)
	changes := []func(*message){
		func(m *message) { m.Session = "old" },
		func(m *message) { m.Instance = "other" },
		func(m *message) { m.Generation++ },
	}
	for _, change := range changes {
		m := right.base
		m.Type, m.Method, m.ID, m.Params = "request", "failed", 100, json.RawMessage(`"old task failure"`)
		change(&m)
		if err := left.receive(m); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-notifications:
		t.Fatal("stale failure delivered")
	default:
	}
	left.mu.Lock()
	stale := left.stale
	left.mu.Unlock()
	if stale != 3 {
		t.Fatal(stale)
	}
}

func TestCanceledBusinessDoesNotEnterHandler(t *testing.T) {
	entered := false
	handler := func(context.Context, Caller, string, json.RawMessage) (any, error) {
		entered = true
		return nil, nil
	}
	i := businessCall(handler, nil, message{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := invokeHandler(ctx, i.call); !errors.Is(err, context.Canceled) || entered {
		t.Fatal("canceled call entered business handler", err)
	}
}
