package events

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

func eventually(t *testing.T, ctx context.Context, fn func() bool) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !fn() {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func TestBridgeTopicRouting(t *testing.T) {
	h := newHarness(t, processSetup{"a", testBridge(t, false), nil}, processSetup{"b", testBridge(t, true), []string{"GORDIS_EV02_EXTRA=1"}})
	h.labels["a"] = "partition"
	_, _, a := h.process("a")
	_, _, b := h.process("b")
	invokeTest(t, h.ctx, a, "Add", testMsg{Global: true})
	if _, err := h.host.Env().Isolate(notice, "partition").MountReady(h.ctx, gordis.InstanceSpec{ID: "partition", Plugin: "partition"}); err != nil {
		t.Fatal(err)
	}
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "default"); err != nil {
		t.Fatal(err)
	}
	if h.count("owner", "received/default") != 1 || h.count("owner", "dynamic/default") != 0 {
		t.Fatal("Global escaped Topic View")
	}
	if err := core.Bind(h.partition).Emit(h.ctx, notice, "isolated"); err != nil {
		t.Fatal(err)
	}
	if h.count("owner", "received/isolated") != 1 || h.count("owner", "dynamic/isolated") != 1 || h.count("host", "received/isolated") != 0 {
		t.Fatal("Host loader Topic View lost")
	}
	invokeTest(t, h.ctx, a, "Emit", testMsg{Value: "from-isolated"})
	if h.count("host", "received/from-isolated") != 0 || h.count("partition", "received/from-isolated") != 1 {
		t.Fatal("Child publisher used Host default View")
	}
	invokeTest(t, h.ctx, b, "Emit", testMsg{Value: "from-default"})
	if h.count("partition", "received/from-default") != 0 || h.count("host", "received/from-default") != 1 {
		t.Fatal("Child to Child partition leaked")
	}
	invokeTest(t, h.ctx, a, "Env", testMsg{})
}

func TestBridgeGenerationAndFailureCleanup(t *testing.T) {
	t.Run("restart", func(t *testing.T) {
		h := newHarness(t)
		old, instance, oldClient := h.process("a")
		if err := instance.Restart(h.ctx); err != nil {
			t.Fatal(err)
		}
		if err := instance.WaitReady(h.ctx); err != nil {
			t.Fatal(err)
		}
		current := <-h.opened["a"]
		if old.Snapshot().Session == current.Snapshot().Session || !old.Snapshot().Reaped || old.EventsSnapshot().Active != 0 {
			t.Fatal("old Session retained", old.Snapshot(), current.Snapshot())
		}
		if _, err := call(h.ctx, oldClient, "Emit", testMsg{Value: "stale"}); err == nil {
			t.Fatal("old client reached new Session")
		}
		if _, err := call(h.ctx, old.peer, "CommitSubscription", idRequest{1}); err == nil {
			t.Fatal("old Commit resurrected subscription")
		}
		if err := core.Bind(h.scope).Emit(h.ctx, notice, "new"); err != nil {
			t.Fatal(err)
		}
		if h.count("owner", "received/new") != 1 || current.EventsSnapshot().Active != 1 {
			t.Fatal("generation subscription duplication")
		}
	})
	for _, mode := range []string{"crash", "kill", "initialization"} {
		t.Run(mode, func(t *testing.T) {
			env := []string{}
			if mode == "initialization" {
				env = append(env, "GORDIS_EV02_INIT_FAIL=1")
			}
			h := newHarness(t, processSetup{"a", testBridge(t, false), env}, processSetup{"b", testBridge(t, true), []string{"GORDIS_EV02_EXTRA=1"}})
			if mode == "initialization" {
				_, err := h.host.Env().MountReady(h.ctx, gordis.InstanceSpec{ID: "a", Plugin: "a"})
				if err == nil {
					t.Fatal("failed initialization became Ready")
				}
				p := <-h.opened["a"]
				if !p.Snapshot().Reaped || p.EventsSnapshot().Pending+p.EventsSnapshot().Committed+p.EventsSnapshot().Active != 0 {
					t.Fatal("startup leaked", p.EventsSnapshot())
				}
				return
			}
			p, _, endpoint := h.process("a")
			_, _, survivor := h.process("b")
			if mode == "kill" {
				process, err := os.FindProcess(p.Snapshot().PID)
				if err != nil {
					t.Fatal(err)
				}
				if err = process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := call(h.ctx, endpoint, "Fail", testMsg{}); err == nil {
					t.Fatal("crash succeeded")
				}
			}
			eventually(t, h.ctx, func() bool { return p.EventsSnapshot().Closed && p.Snapshot().Reaped })
			if p.EventsSnapshot().Active+p.EventsSnapshot().Pending+p.EventsSnapshot().Committed != 0 {
				t.Fatal("crash left proxies", p.EventsSnapshot())
			}
			d := p.Snapshot()
			if d.Cleanup == nil || d.Cleanup.CleanupComplete || !d.Cleanup.Forced {
				t.Fatal("lost unknown cleanup", d)
			}
			invokeTest(t, h.ctx, survivor, "Emit", testMsg{Value: "survivor"})
			if h.count("owner", "received/survivor") != 1 {
				t.Fatal("failed Child affected other Session")
			}
		})
	}
}

func TestBridgeCancellationAndDrain(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "kill"}[kill], func(t *testing.T) {
			h := newHarness(t)
			p, _, endpoint := h.process("a")
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			_, err := core.Bind(h.scope).On(notice, func(ctx context.Context, msg string) error {
				if msg != "host-hold" {
					return nil
				}
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				return ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			c, cancel := context.WithCancel(h.ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := call(c, endpoint, "Emit", testMsg{Value: "host-hold"}); done <- err }()
			if err := waitGate(h.ctx, entered); err != nil {
				t.Fatal(err)
			}
			cancel()
			if err := waitGate(h.ctx, canceled); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			if d := p.Snapshot(); d.Admission.Business.Handling != 1 || d.CallbackHandling == 0 {
				t.Fatal("caller cancellation lost actual callback", d)
			}
			if p.EventsSnapshot().Dispatching != 1 {
				t.Fatal("actual event dispatch disappeared")
			}
			if kill {
				process, err := os.FindProcess(p.Snapshot().PID)
				if err != nil {
					t.Fatal(err)
				}
				if err = process.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			stopped := make(chan error, 1)
			go func() { stopped <- h.host.Shutdown(h.ctx) }()
			eventually(t, h.ctx, func() bool { return p.EventsSnapshot().Closed })
			if e := p.EventsSnapshot(); e.Active+e.Committed+e.Pending != 0 || e.Dispatching != 1 {
				t.Fatal("false domain drain", e)
			}
			select {
			case err := <-stopped:
				t.Fatal("shutdown passed actual handler", err)
			default:
			}
			unblock()
			select {
			case err := <-stopped:
				if !kill && err != nil {
					t.Fatal(err)
				}
				if kill && err == nil {
					t.Fatal("kill reported clean")
				}
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			d, e := p.Snapshot(), p.EventsSnapshot()
			if !d.Reaped || !d.CallbacksDrained || d.Admission != (rpc.AdmissionSnapshot{}) || e.Dispatching+e.Delivering != 0 {
				t.Fatal("not drained", d, e)
			}
			if kill && (d.Cleanup == nil || d.Cleanup.CleanupComplete || !d.Cleanup.Forced) {
				t.Fatal("unknown cleanup forgotten", d)
			}
		})
	}
}

func TestBridgeOnceFailureReclaimsBothSides(t *testing.T) {
	b := testBridge(t, false)
	binding := b.bindings[notice.ID()]
	encode := binding.encode
	binding.encode = func(msg any) (json.RawMessage, error) {
		if msg == "bad-codec" {
			return nil, ErrCodec
		}
		return encode(msg)
	}
	b.bindings[notice.ID()] = binding
	h := newHarness(t, processSetup{"a", b, nil})
	p, _, endpoint := h.process("a")
	invokeTest(t, h.ctx, endpoint, "Add", testMsg{Once: true, Priority: 100})
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "bad-codec"); !errors.Is(err, ErrCodec) {
		t.Fatal(err)
	}
	if p.EventsSnapshot().Active != 1 {
		t.Fatal("Once failed encoding leaked Host mapping", p.EventsSnapshot())
	}
	invokeTest(t, h.ctx, endpoint, "Inspect", testMsg{})
	h.await("subscriptions/1")
	if h.count("owner", "dynamic/bad-codec") != 0 {
		t.Fatal("invalid codec invoked Child")
	}
}

func TestBridgeSubscribeStopRace(t *testing.T) {
	b := testBridge(t, false)
	allocated, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	count := 0
	interceptHost(b, func(ctx context.Context, method string, raw json.RawMessage, peer service) (reply, error) {
		out, err := peer.invoke(ctx, method, raw)
		if method == "Subscribe" {
			mu.Lock()
			count++
			dynamic := count == 2
			mu.Unlock()
			if dynamic {
				close(allocated)
				if e := waitGate(ctx, release); e != nil {
					return reply{}, e
				}
			}
		}
		return out, err
	})
	h := newHarness(t, processSetup{"a", b, nil})
	p, _, endpoint := h.process("a")
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	added := make(chan error, 1)
	go func() { _, err := call(h.ctx, endpoint, "Add", testMsg{}); added <- err }()
	if err := waitGate(h.ctx, allocated); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.host.Shutdown(h.ctx) }()
	eventually(t, h.ctx, func() bool { return p.EventsSnapshot().Closed })
	if p.EventsSnapshot().Pending+p.EventsSnapshot().Active+p.EventsSnapshot().Committed != 0 {
		t.Fatal("stop retained pending subscription")
	}
	unblock()
	select {
	case err := <-added:
		if err == nil {
			t.Fatal("stopping subscription reported success")
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal("normal race cleanup failed", err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	d := p.Snapshot()
	if !d.Reaped || d.Cleanup == nil || !d.Cleanup.CleanupComplete || !d.CallbacksDrained {
		t.Fatal("late response leaked", d)
	}
}

func TestBridgeUnconfirmedRemovalFailsSession(t *testing.T) {
	b := testBridge(t, false)
	interceptHost(b, func(ctx context.Context, method string, raw json.RawMessage, peer service) (reply, error) {
		if method == "Unsubscribe" {
			return reply{}, context.DeadlineExceeded
		}
		return peer.invoke(ctx, method, raw)
	})
	h := newHarness(t, processSetup{"a", b, nil})
	p, _, endpoint := h.process("a")
	// func() disables Child delivery immediately; an unconfirmed wire removal
	// must become a managed failure, not a silently successful cancellation.
	_, _ = call(h.ctx, endpoint, "Remove", testMsg{})
	eventually(t, h.ctx, func() bool { return p.EventsSnapshot().Closed && p.Snapshot().Reaped })
	d := p.Snapshot()
	if p.EventsSnapshot().Active != 0 || d.Cleanup == nil || d.Cleanup.CleanupComplete {
		t.Fatal("unconfirmed removal reported complete", d, p.EventsSnapshot())
	}
}

func TestBridgeHungCleanupAndTaskFailure(t *testing.T) {
	t.Run("hung-cleanup", func(t *testing.T) {
		h := newHarness(t, processSetup{"a", testBridge(t, false), []string{"GORDIS_EV02_HUNG_CLEANUP=1"}})
		p, _, _ := h.process("a")
		stopped := make(chan error, 1)
		go func() { stopped <- h.host.Shutdown(h.ctx) }()
		h.await("cleanup-entered")
		e := p.EventsSnapshot()
		if !e.Closed || e.Active+e.Pending+e.Committed != 0 {
			t.Fatal("hung cleanup retained Host proxy", e)
		}
		select {
		case err := <-stopped:
			t.Fatal("cleanup barrier bypassed", err)
		default:
		}
		select {
		case err := <-stopped:
			if err == nil {
				t.Fatal("hung cleanup reported success")
			}
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
		d := p.Snapshot()
		if !d.Reaped || d.Cleanup == nil || d.Cleanup.CleanupComplete || !d.Cleanup.Forced || !d.CallbacksDrained {
			t.Fatal("hung cleanup evidence lost", d)
		}
	})
	t.Run("task-failure", func(t *testing.T) {
		h := newHarness(t)
		p, _, endpoint := h.process("a")
		_, _ = call(h.ctx, endpoint, "FailTask", testMsg{})
		eventually(t, h.ctx, func() bool { return p.EventsSnapshot().Closed && p.Snapshot().Reaped })
		if p.EventsSnapshot().Active+p.EventsSnapshot().Pending+p.EventsSnapshot().Committed != 0 {
			t.Fatal("task failure retained subscriptions")
		}
	})
}

func TestBridgeCodecAdmissionBeforeSerialization(t *testing.T) {
	b := testBridge(t, false)
	entry := b.bindings[notice.ID()]
	encode := entry.encode
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var mu sync.Mutex
	encodings := 0
	entry.encode = func(msg any) (json.RawMessage, error) {
		mu.Lock()
		encodings++
		first := encodings == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return encode(msg)
	}
	b.bindings[notice.ID()] = entry
	h := newHarness(t, processSetup{"a", b, []string{"GORDIS_EV02_LIMIT_ONE=1"}})
	p, _, _ := h.process("a")
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- core.Bind(h.scope).Emit(ctx, notice, "serialize") }()
	if err := waitGate(h.ctx, entered); err != nil {
		t.Fatal(err)
	}
	if p.Snapshot().Admission.Business.Pending != 1 {
		t.Fatal("EventCodec ran before Pending admission", p.Snapshot())
	}
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "second"); !errors.Is(err, rpc.ErrBackpressure) {
		t.Fatal("second encoding admitted", err)
	}
	mu.Lock()
	count := encodings
	mu.Unlock()
	if count != 1 {
		t.Fatal("backpressured call ran codec", count)
	}
	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- h.host.Shutdown(h.ctx) }()
	eventually(t, h.ctx, func() bool { return p.EventsSnapshot().Closed })
	if p.Snapshot().Admission.Business.Pending != 1 || p.EventsSnapshot().Delivering != 1 {
		t.Fatal("cancellation released actual encoder")
	}
	select {
	case err := <-stopped:
		t.Fatal("cleanup passed blocked codec", err)
	default:
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	if p.Snapshot().Admission != (rpc.AdmissionSnapshot{}) || p.EventsSnapshot().Delivering != 0 || !p.Snapshot().Reaped {
		t.Fatal("encoder not drained", p.Snapshot(), p.EventsSnapshot())
	}
}

func TestBridgeEventOnlyUnit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := testBridge(t, false).NewProcess(rpc.ProcessOptions{PluginID: "event-only", Command: rpc.Command{Path: os.Args[0], Args: []string{"-test.run=^TestBridgeChild$"}, Env: []string{"GORDIS_EV02_CHILD=1", "GORDIS_EV02_EVENT_ONLY=1"}}, Identity: testIdentity(), Limits: bridgeFixtureLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Spec().Provides) != 0 || len(p.Spec().Requires) != 1 {
		t.Fatal("private endpoint leaked into business providers", p.Spec())
	}
	opened := make(chan *ProcessPlugin, 4)
	host, err := gordis.NewHost(&core.Plugin{}, &capturedProcess{prototype: p, opened: opened})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown(context.Background())
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "bus", Plugin: core.PluginID}); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "events", Plugin: "event-only"}); err != nil {
		t.Fatal(err)
	}
	actual := <-opened
	if err = host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	d := actual.Snapshot()
	if !d.Reaped || d.Cleanup == nil || !d.Cleanup.CleanupComplete || actual.EventsSnapshot().Owners != 0 {
		t.Fatal("event-only cleanup", d, actual.EventsSnapshot())
	}
}
