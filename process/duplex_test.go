package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for callback")
		var zero T
		return zero
	}
}

func relay(method string, value any) any {
	return struct {
		Method string
		Value  any
	}{method, value}
}

func TestDuplexInitializationNestedCallsAndCorrelation(t *testing.T) {
	initialized := false
	handler := func(ctx context.Context, plugin Caller, method string, raw json.RawMessage) (any, error) {
		if method == "host.init" {
			initialized = true // Start waits for this callback and initialize's response.
			return nil, nil
		}
		var value int
		if err := plugin.Call(ctx, "identity", raw, &value); err != nil {
			return nil, err
		}
		return value + 1, nil
	}
	c, err := startChild(t, "duplex", Limits{MaxPending: 128, Queue: 128}, handler)
	if err != nil || !initialized {
		t.Fatal(err, initialized)
	}
	var wg sync.WaitGroup
	for n := 0; n < 24; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var got int
			// Host -> plugin -> host -> plugin, with overlapping IDs in each direction.
			if err := c.Call(ctx, "relay", relay("nested", n), &got); err != nil || got != n+1 {
				t.Errorf("%d: got %d, %v", n, got, err)
			}
		}(n)
	}
	wg.Wait()
}

func TestDuplexErrorsAndCancellation(t *testing.T) {
	entered, canceled := make(chan struct{}, 4), make(chan struct{}, 4)
	handler := func(ctx context.Context, _ Caller, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "wait":
			entered <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return nil, ctx.Err()
		case "panic":
			panic("host panic")
		default:
			return nil, &RPCError{Code: "denied", Message: "capability unavailable"}
		}
	}
	c, err := startChild(t, "normal", Limits{}, handler)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"denied", "panic"} {
		var rpc *RPCError
		err := c.Call(context.Background(), "relay", relay(method, nil), nil)
		want := method
		if method == "panic" {
			want = "handler"
		}
		if !errors.As(err, &rpc) || rpc.Code != want {
			t.Fatal(err)
		}
	}
	var rpc *RPCError
	if err := c.Call(context.Background(), "panic", nil, nil); !errors.As(err, &rpc) || rpc.Code != "handler" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background()) // no deadline: wire cancel must propagate
	done := make(chan error, 1)
	go func() { done <- c.Call(ctx, "relay", relay("wait", nil), nil) }()
	await(t, entered)
	cancel()
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	await(t, canceled)
	var timedOut bool
	if err := c.Call(context.Background(), "cancel-host", nil, &timedOut); err != nil || !timedOut {
		t.Fatal(timedOut, err)
	}
	await(t, entered)
	await(t, canceled)
	if err := c.Call(context.Background(), "identity", 7, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForHostHandlers(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	handler := func(ctx context.Context, _ Caller, _ string, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release // Simulates a host resource that cannot be reclaimed yet.
		return nil, ctx.Err()
	}
	c, err := startChild(t, "normal", Limits{}, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer unblock() // before startChild's cleanup, including on failure
	done := make(chan error, 1)
	go func() { done <- c.Call(context.Background(), "relay", relay("wait", nil), nil) }()
	await(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	await(t, canceled)
	if c.Snapshot().HandlersComplete || c.Snapshot().Handling != 1 {
		t.Fatal(c.Snapshot())
	}
	unblock()
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := await(t, done); err == nil {
		t.Fatal("closed call succeeded")
	}
	if !c.Snapshot().HandlersComplete {
		t.Fatal(c.Snapshot())
	}
	if c.Wait() != nil {
		t.Fatal("explicit close reported a failure")
	}
}

func TestStartupFailureAndCrashCancelHostHandlers(t *testing.T) {
	t.Run("startup", func(t *testing.T) {
		canceled := make(chan struct{})
		handler := func(ctx context.Context, _ Caller, _ string, _ json.RawMessage) (any, error) {
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
		c, err := startChild(t, "callback-init-failure", Limits{}, handler)
		if err == nil || c == nil {
			t.Fatal("startup must fail", err)
		}
		await(t, canceled)
		if !c.Snapshot().HandlersComplete {
			t.Fatal(c.Snapshot())
		}
	})
	t.Run("crash", func(t *testing.T) {
		entered, canceled := make(chan struct{}), make(chan struct{})
		handler := func(ctx context.Context, _ Caller, _ string, _ json.RawMessage) (any, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
		c, err := startChild(t, "normal", Limits{}, handler)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- c.Call(context.Background(), "relay", relay("wait", nil), nil) }()
		await(t, entered)
		if err := c.Call(context.Background(), "crash", nil, nil); err == nil {
			t.Fatal("crash ignored")
		}
		await(t, canceled)
		if err := await(t, done); err == nil {
			t.Fatal("interrupted call succeeded")
		}
	})
}

func TestHostCapabilitiesAreExplicitAndSessionBound(t *testing.T) {
	var retained Caller
	handler := func(_ context.Context, caller Caller, method string, _ json.RawMessage) (any, error) {
		retained = caller
		if method != "host.init" {
			return nil, fmt.Errorf("unexpected %s", method)
		}
		return nil, nil
	}
	c, err := startChild(t, "duplex", Limits{}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := retained.Call(context.Background(), "identity", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	c, err = startChild(t, "normal", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var rpc *RPCError
	err = c.Call(context.Background(), "relay", relay("store.put", nil), nil)
	if !errors.As(err, &rpc) || rpc.Code != "method" {
		t.Fatal(err)
	}
}

func TestHostHandlerWaitsForIdentityValidation(t *testing.T) {
	called := make(chan struct{}, 1)
	_, err := startChild(t, "early", Limits{}, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		called <- struct{}{}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("host handler ran before identity validation")
	default:
	}
}
