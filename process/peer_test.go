package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func connectedPeers(t *testing.T, limits Limits, leftHandler, rightHandler CallHandler) (*peer, *peer) {
	t.Helper()
	leftIn, rightOut := io.Pipe()
	rightIn, leftOut := io.Pipe()
	base := message{Session: "session", Instance: "alpha", Generation: 1}
	left := newPeer(leftIn, leftOut, limits, base)
	right := newPeer(rightIn, rightOut, Limits{MaxPending: 64, Queue: 64}, base)
	left.accept = func(m message) (invocation, error) { return businessCall(leftHandler, left, m), nil }
	right.accept = func(m message) (invocation, error) { return businessCall(rightHandler, right, m), nil }
	t.Cleanup(func() {
		left.close(ErrClosed)
		right.close(ErrClosed)
		left.workers.Wait()
		right.workers.Wait()
		left.handlers.Wait()
		right.handlers.Wait()
	})
	left.start()
	right.start()
	return left, right
}

func TestDuplexIndependentLimitsAndCancellationIDs(t *testing.T) {
	leftEntered, rightEntered := make(chan struct{}), make(chan struct{})
	leftCanceled, rightCanceled := make(chan struct{}), make(chan struct{})
	handler := func(entered, canceled chan struct{}) CallHandler {
		return func(ctx context.Context, _ Caller, method string, raw json.RawMessage) (any, error) {
			if method != "hold" {
				return raw, nil
			}
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
	}
	left, right := connectedPeers(
		t,
		Limits{MaxPending: 1},
		handler(leftEntered, leftCanceled),
		handler(rightEntered, rightCanceled),
	)
	leftCtx, cancelLeft := context.WithCancel(context.Background())
	rightCtx, cancelRight := context.WithCancel(context.Background())
	defer cancelLeft()
	defer cancelRight()
	leftResult, rightResult := make(chan error, 1), make(chan error, 1)
	// Both directions use ID 1. Each side has a separate incoming/outgoing budget.
	go func() { leftResult <- left.Call(leftCtx, "hold", nil, nil) }()
	go func() { rightResult <- right.Call(rightCtx, "hold", nil, nil) }()
	await(t, leftEntered)
	await(t, rightEntered)
	if err := left.Call(context.Background(), "echo", nil, nil); !errors.Is(err, ErrBackpressure) {
		t.Fatal(err)
	}
	var rpc *RPCError
	if err := right.Call(context.Background(), "echo", nil, nil); !errors.As(err, &rpc) || rpc.Code != "backpressure" {
		t.Fatal(err)
	}
	cancelRight()
	if err := await(t, rightResult); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	await(t, leftCanceled)
	select {
	case <-rightCanceled:
		t.Fatal("cancellation reached the wrong origin's ID")
	default:
	}
	cancelLeft()
	if err := await(t, leftResult); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	await(t, rightCanceled)
}

func TestStaleRequestsAndDuplicateIDs(t *testing.T) {
	called := make(chan struct{}, 2)
	left, right := connectedPeers(t, Limits{}, func(context.Context, Caller, string, json.RawMessage) (any, error) {
		called <- struct{}{}
		return nil, nil
	}, nil)
	for _, change := range []func(*message){
		func(m *message) { m.Session = "old" },
		func(m *message) { m.Instance = "other" },
		func(m *message) { m.Generation++ },
	} {
		m := right.base
		m.Type, m.Method, m.ID = "request", "write", 100
		change(&m)
		if err := left.receive(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := right.Call(context.Background(), "write", nil, nil); err != nil {
		t.Fatal(err)
	}
	await(t, called)
	select {
	case <-called:
		t.Fatal("stale request executed")
	default:
	}
	duplicate := right.base
	duplicate.Type, duplicate.Method, duplicate.ID = "request", "write", 1
	if err := right.enqueue(duplicate, nil); err != nil {
		t.Fatal(err)
	}
	await(t, left.ctx.Done())
	select {
	case <-called:
		t.Fatal("duplicate request executed")
	default:
	}
}
