package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

var choice = core.NewHook[string, string]("ev03/choice")
var transform = core.NewHook[string, string]("ev03/transform")
var internalSource = gordis.NewKey[string]("ev03/internal")

func hookBridge(t *testing.T) *Bridge {
	t.Helper()
	codec := JSONEventCodec[string]("string/1")
	b, err := NewBridge(BindTopic(notice, codec, Publish|Subscribe), BindHook(choice, codec, codec, Publish|Subscribe), BindHook(transform, codec, codec, Publish|Subscribe))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func installHooks(s *gordis.Scope, id string, late *savedNext) error {
	_, err := core.Bind(s).Handle(choice, func(ctx context.Context, in string) (string, bool, error) {
		switch in {
		case "select":
			return id + " selected", true, nil
		case "error":
			return id + " failed", true, gordis.ErrStale
		case "panic":
			panic("hook panic")
		}
		return "", false, nil
	})
	if err != nil {
		return err
	}
	_, err = core.Bind(s).Use(transform, func(ctx context.Context, in string, next core.Next[string, string]) (string, error) {
		switch in {
		case "short":
			return id + " short", nil
		case "late":
			late.save(next)
			return "saved", nil
		case "twice":
			if _, err := next(ctx, in); err != nil {
				return "", err
			}
			return next(ctx, in)
		case "base-hold":
			return next(ctx, in)
		case "async":
			go func() { _, _ = next(ctx, "async-base") }()
			host, err := s.Get(testHostKey)
			if err != nil {
				return "", err
			}
			if _, err = call(ctx, host, "Signal", testMsg{Value: "async-wait"}); err != nil {
				return "", err
			}
			return "early", nil
		}
		v, err := next(ctx, in+"/"+id)
		return id + "<" + v + ">", err
	})
	return err
}
func hookSetup(t *testing.T, id string, multi bool) processSetup {
	env := []string{"GORDIS_EV03_HOOKS=1"}
	if multi {
		env = append(env, "GORDIS_EV02_MULTI_OWNER=1")
	}
	return processSetup{id, hookBridge(t), env}
}
func hookCall(ctx context.Context, endpoint service, method, input string, b Binding) (core.RemoteResult, error) {
	return callResult(ctx, endpoint, method, testMsg{Value: input}, b, true)
}
func TestBridgeSelectionAndWaterfall(t *testing.T) {
	h := newHarness(t, hookSetup(t, "a", false), hookSetup(t, "b", false))
	a, _, client := h.process("a")
	b, _, _ := h.process("b")
	ev := core.Bind(h.scope)
	// Local selection has the same priority and handled semantics as proxies.
	var selected int
	_, err := ev.Handle(choice, func(context.Context, string) (string, bool, error) { selected++; return "skip", false, nil }, core.Priority(100))
	if err != nil {
		t.Fatal(err)
	}
	for _, fromChild := range []bool{false, true} {
		var r core.RemoteResult
		var err error
		if fromChild {
			r, err = hookCall(h.ctx, client, "Serial", "select", a.peer.bridge.bindings[choice.ID()])
		} else {
			r.Value, r.Handled, err = ev.Bail(h.ctx, choice, "select")
		}
		if err != nil || !r.Handled || r.Value != "owner selected" {
			t.Fatal(r, err)
		}
		if fromChild {
			r, err = hookCall(h.ctx, client, "Serial", "none", a.peer.bridge.bindings[choice.ID()])
		} else {
			r.Value, r.Handled, err = ev.Serial(h.ctx, choice, "none")
		}
		if r.Handled || !errors.Is(err, core.ErrNoHandler) {
			t.Fatal(r, err)
		}
		if fromChild {
			r, err = hookCall(h.ctx, client, "Serial", "error", a.peer.bridge.bindings[choice.ID()])
		} else {
			r.Value, r.Handled, err = ev.Serial(h.ctx, choice, "error")
		}
		if !errors.Is(err, gordis.ErrStale) || !r.Handled || r.Value != "owner failed" {
			t.Fatal("business error/result", r, err)
		}
	}
	if selected != 6 {
		t.Fatal(selected)
	}
	var late savedNext
	_, err = ev.Use(transform, func(ctx context.Context, in string, next core.Next[string, string]) (string, error) {
		late.save(next)
		v, err := next(ctx, in+"/host")
		return "host<" + v + ">", err
	}, core.Priority(100))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ev.Waterfall(h.ctx, transform, "in", func(_ context.Context, in string) (string, error) { return "base(" + in + ")", nil })
	want := "host<owner<owner<base(in/host/owner/owner)>>>"
	if err != nil || got != want {
		t.Fatal(got, want, err)
	}
	if _, err = late.call(h.ctx, "late"); !errors.Is(err, core.ErrNextExpired) {
		t.Fatal(err)
	}
	r, err := hookCall(h.ctx, client, "Waterfall", "in", a.peer.bridge.bindings[transform.ID()])
	if err != nil || r.Value != want {
		t.Fatal(r, want, err)
	}
	// All three Session directions participated; the Child base was reached via Continue.
	if a.EventsSnapshot().Tokens+b.EventsSnapshot().Tokens != 0 {
		t.Fatal(a.EventsSnapshot(), b.EventsSnapshot())
	}
	if err := h.host.Shutdown(h.ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*ProcessPlugin{a, b} {
		d := p.Snapshot()
		if !d.Reaped || !d.CallbacksDrained || d.Cleanup == nil || !d.Cleanup.CleanupComplete || p.EventsSnapshot().Tokens != 0 {
			t.Fatal(d, p.EventsSnapshot())
		}
	}
}
func TestBridgeContinuationLifetime(t *testing.T) {
	h := newHarness(t, hookSetup(t, "a", false))
	p, instance, endpoint := h.process("a")
	b := p.peer.bridge.bindings[transform.ID()]
	base := core.Next[string, string](func(_ context.Context, in string) (string, error) { return in, nil })
	if _, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "twice", base); !errors.Is(err, core.ErrNextCalled) {
		t.Fatal(err)
	}
	if v, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "late", base); err != nil || v != "saved" {
		t.Fatal(v, err)
	}
	if _, err := hookCall(h.ctx, endpoint, "Late", "after-return", b); !errors.Is(err, core.ErrNextExpired) {
		t.Fatal(err)
	}
	if v, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "short", func(context.Context, string) (string, error) {
		t.Fatal("base called after short circuit")
		return "", nil
	}); err != nil || v != "owner short" {
		t.Fatal(v, err)
	}
	// Replay a captured token after its invocation, then after generation replacement.
	var raw json.RawMessage
	p.peer.mu.Lock()
	original := p.peer.endpoint
	p.peer.endpoint = testService(func(ctx context.Context, m string, r json.RawMessage) (reply, error) {
		if m == "Deliver" {
			raw = append(json.RawMessage(nil), r...)
		}
		// Preserve typed result decoding when observing a production client.
		if m == "Deliver" {
			out, err := callResult(ctx, original, m, r, b, true)
			return resultReply(b, out, err)
		}
		return original.invoke(ctx, m, r)
	})
	p.peer.mu.Unlock()
	if _, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "capture", base); err != nil {
		t.Fatal(err)
	}
	var msg deliverRequest
	if err := decodeWire(raw, &msg); err != nil {
		t.Fatal(err)
	}
	p.peer.mu.Lock()
	p.peer.endpoint = original
	p.peer.mu.Unlock()
	for _, contract := range []string{transform.ID(), choice.ID()} {
		_, err := call(h.ctx, p.peer, "Continue", continueRequest{msg.Token, contract, json.RawMessage(`"late"`)})
		if !errors.Is(err, core.ErrNextExpired) {
			t.Fatal(err)
		}
	}
	if err := instance.Restart(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := instance.WaitReady(h.ctx); err != nil {
		t.Fatal(err)
	}
	current := <-h.opened["a"]
	if current.Snapshot().Session == p.Snapshot().Session || !p.Snapshot().Reaped || p.EventsSnapshot().Tokens != 0 {
		t.Fatal(p.Snapshot(), current.Snapshot())
	}
	if _, err := hookCall(h.ctx, endpoint, "Late", "old", b); err == nil {
		t.Fatal("old client reached replacement")
	}
	if _, err := call(h.ctx, p.peer, "Continue", continueRequest{msg.Token, msg.Contract, json.RawMessage(`"old"`)}); !errors.Is(err, core.ErrNextExpired) {
		t.Fatal(err)
	}
	if v, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "new", base); err != nil || v != "owner<new/owner>" {
		t.Fatal(v, err)
	}

}

func TestBridgeContinuationCapacity(t *testing.T) {
	b := hookBridge(t).bindings[transform.ID()]
	var table tokenTable
	testCtx, testCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer testCancel()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	id, err := table.add(ctx, core.OwnerRef{Instance: "owner", Generation: 1}, b, func(ctx context.Context, v any) (any, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := call(context.Background(), testService(func(ctx context.Context, _ string, raw json.RawMessage) (reply, error) { return table.invoke(ctx, raw) }), "Continue", continueRequest{id, transform.ID(), json.RawMessage(`"in"`)})
		done <- err
	}()
	if err := waitGate(testCtx, entered); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < maxTokens; i++ {
		if _, err := table.add(context.Background(), core.OwnerRef{}, b, func(context.Context, any) (any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	eventually(t, testCtx, func() bool { total, running := table.snapshot(); return total == maxTokens && running == 1 })
	if _, err := table.add(context.Background(), core.OwnerRef{}, b, nil); !errors.Is(err, rpc.ErrBackpressure) {
		t.Fatal(err)
	}
	if _, err := table.invoke(context.Background(), mustRaw(t, continueRequest{id, transform.ID(), json.RawMessage(`"again"`)})); !errors.Is(err, core.ErrNextExpired) {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if id2, err := table.add(context.Background(), core.OwnerRef{}, b, func(context.Context, any) (any, error) { return nil, nil }); err != nil || id2 <= maxTokens {
		t.Fatal(id2, err)
	}
	table.revoke(nil)
	if total, running := table.snapshot(); total+running != 0 {
		t.Fatal(total, running)
	}
}
func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	r, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBridgeSourceRouting(t *testing.T) {
	h := newHarness(t, hookSetup(t, "a", true), hookSetup(t, "b", false))
	p, _, a := h.process("a")
	_, _, b := h.process("b")
	if _, err := h.host.Env().Isolate(testChildKey, "a").MountReady(h.ctx, gordis.InstanceSpec{ID: "partition", Plugin: "partition"}); err != nil {
		t.Fatal(err)
	}
	invokeTest(t, h.ctx, a, "PublishFrom", testMsg{Value: "a-source"})
	if h.count("partition", "received/a-source") != 1 || h.count("host", "received/a-source") != 0 || h.count("owner", "received/a-source") != 1 || h.count("second", "received/a-source") != 1 {
		t.Fatal("source label routing", h.messages)
	}
	invokeTest(t, h.ctx, b, "EmitFrom", testMsg{Value: "b-source"})
	if h.count("partition", "received/b-source") != 0 || h.count("host", "received/b-source") != 0 || h.count("owner", "received/b-source") != 1 {
		t.Fatal("source label escaped", h.messages)
	}
	// Global bypasses source filtering while Topic partitions remain enforced by EV02.
	invokeTest(t, h.ctx, a, "Add", testMsg{Global: true})
	invokeTest(t, h.ctx, b, "EmitFrom", testMsg{Value: "global"})
	if h.count("owner", "dynamic/global") != 1 {
		t.Fatal("Global failed to bypass source partition")
	}
	if err := core.Bind(h.scope).EmitFrom(h.ctx, testHostKey, notice, "host-source"); err != nil {
		t.Fatal(err)
	}
	if h.count("owner", "received/host-source") != 2 || h.count("second", "received/host-source") != 1 {
		t.Fatal("Host source did not reach Child owners")
	}
	for _, method := range []string{"SecondFrom", "InternalFrom"} {
		_, err := call(h.ctx, a, method, testMsg{Value: "forbidden"})
		if err == nil {
			t.Fatal("unauthorized owner/source", method)
		}
		if method == "InternalFrom" && !errors.Is(err, ErrInvalidSource) {
			t.Fatal(err)
		}
	}
	forged := "host/forged"
	_, err := call(h.ctx, p.peer, "Dispatch", dispatchRequest{Owner: core.OwnerRef{Instance: "owner", Generation: 1}, Contract: notice.ID(), Mode: core.RemoteEmit, Payload: json.RawMessage(`"forged"`), Source: &forged})
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatal("Host trusted forged source", err)
	}
	// Global Hook selection also bypasses a different Child's source label.
	invokeTest(t, h.ctx, a, "AddHook", testMsg{Value: "select", Global: true, Priority: 100})
	if result, err := hookCall(h.ctx, b, "SerialFrom", "select", p.peer.bridge.bindings[choice.ID()]); err != nil || result.Value != "dynamic" {
		t.Fatal(result, err)
	}
	// Hook *From uses the same exact source checks and routing address.
	for _, method := range []string{"SerialFrom", "WaterfallFrom"} {
		input := "select"
		if method == "WaterfallFrom" {
			input = "in"
		}
		binding := p.peer.bridge.bindings[choice.ID()]
		if method == "WaterfallFrom" {
			binding = p.peer.bridge.bindings[transform.ID()]
		}
		result, err := hookCall(h.ctx, a, method, input, binding)
		if err != nil || !result.Handled {
			t.Fatal(method, result, err)
		}
	}
	if _, handled, err := core.Bind(h.scope).SerialFrom(h.ctx, testHostKey, choice, "select"); err != nil || !handled {
		t.Fatal(handled, err)
	}
	if _, err := core.Bind(h.scope).WaterfallFrom(h.ctx, testHostKey, transform, "in", func(_ context.Context, in string) (string, error) { return in, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeContinuationCancellationAndDrain(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(fmt.Sprint("kill=", kill), func(t *testing.T) {
			h := newHarness(t, hookSetup(t, "a", false))
			p, _, _ := h.process("a")
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			c, cancel := context.WithCancel(h.ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := core.Bind(h.scope).Waterfall(c, transform, "base-hold", func(ctx context.Context, in string) (string, error) {
					close(entered)
					<-ctx.Done()
					close(canceled)
					<-release
					return "", ctx.Err()
				})
				done <- err
			}()
			if err := waitGate(h.ctx, entered); err != nil {
				t.Fatal(err)
			}
			cancel()
			if err := waitGate(h.ctx, canceled); err != nil {
				t.Fatal(err)
			}
			eventually(t, h.ctx, func() bool {
				return p.EventsSnapshot().Continuing == 1 && p.Snapshot().Admission.Continuation.Handling == 1
			})
			if e := p.EventsSnapshot(); e.Tokens != 1 || e.Delivering != 1 {
				t.Fatal("expired token lost actual work", e)
			}
			select {
			case err := <-done:
				t.Fatal("dispatch passed actual next", err)
			default:
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
			if p.EventsSnapshot().Tokens != 1 || p.Snapshot().CallbackHandling == 0 {
				t.Fatal("false continuation drain", p.EventsSnapshot(), p.Snapshot())
			}
			select {
			case err := <-stopped:
				t.Fatal("shutdown passed actual continuation", err)
			default:
			}
			unblock()
			select {
			case <-done:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			select {
			case err := <-stopped:
				if (err == nil) == kill {
					t.Fatal("cleanup status", kill, err)
				}
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			d, e := p.Snapshot(), p.EventsSnapshot()
			if !d.Reaped || !d.CallbacksDrained || d.Admission != (rpc.AdmissionSnapshot{}) || e.Tokens+e.Continuing+e.Delivering != 0 {
				t.Fatal(d, e)
			}
			if d.Cleanup == nil || d.Cleanup.CleanupComplete == kill {
				t.Fatal("remote cleanup evidence", d)
			}
		})
	}
}

type savedNext struct {
	mu sync.Mutex
	fn core.Next[string, string]
}

func (s *savedNext) save(fn core.Next[string, string]) { s.mu.Lock(); s.fn = fn; s.mu.Unlock() }
func (s *savedNext) call(ctx context.Context, in string) (string, error) {
	s.mu.Lock()
	fn := s.fn
	s.mu.Unlock()
	if fn == nil {
		return "", core.ErrNextExpired
	}
	return fn(ctx, in)
}

func TestBridgeMiddlewareReturnDrainsAdmittedNext(t *testing.T) {
	b := hookBridge(t)
	entered, expired, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var token tokenRequest
	interceptHost(b, func(ctx context.Context, method string, raw json.RawMessage, peer service) (reply, error) {
		out, err := peer.invoke(ctx, method, raw)
		if method == "ExpireContinuation" {
			if e := decodeWire(raw, &token); e != nil {
				return reply{}, e
			}
			close(expired)
		}
		return out, err
	})
	setup := hookSetup(t, "a", false)
	setup.bridge = b
	h := newHarness(t, setup)
	h.gate = func(ctx context.Context, msg testMsg) error {
		if msg.Value == "async-wait" {
			return waitGate(ctx, entered)
		}
		return nil
	}
	p, _, _ := h.process("a")
	done := make(chan error, 1)
	go func() {
		v, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "async", func(ctx context.Context, in string) (string, error) { close(entered); <-release; return in, nil })
		if err == nil && v != "early" {
			err = fmt.Errorf("unexpected result %s", v)
		}
		done <- err
	}()
	if err := waitGate(h.ctx, expired); err != nil {
		t.Fatal(err)
	}
	if e := p.EventsSnapshot(); e.Tokens != 1 || e.Continuing != 1 {
		t.Fatal(e)
	}
	if _, err := call(h.ctx, p.peer, "Continue", continueRequest{token.Token, token.Contract, json.RawMessage(`"late"`)}); !errors.Is(err, core.ErrNextExpired) {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatal("returned before next drain", err)
	default:
	}
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	if e := p.EventsSnapshot(); e.Tokens+e.Continuing != 0 {
		t.Fatal(e)
	}
}

func TestBridgeHookOptions(t *testing.T) {
	h := newHarness(t, hookSetup(t, "a", false))
	p, _, endpoint := h.process("a")
	invokeTest(t, h.ctx, endpoint, "AddHook", testMsg{Value: "select", Once: true, Priority: 100, Global: true})
	if v, handled, err := core.Bind(h.scope).Serial(h.ctx, choice, "select"); err != nil || !handled || v != "dynamic" {
		t.Fatal(v, handled, err)
	}
	if v, handled, err := core.Bind(h.scope).Serial(h.ctx, choice, "select"); err != nil || !handled || v != "owner selected" {
		t.Fatal(v, handled, err)
	}
	invokeTest(t, h.ctx, endpoint, "AddHook", testMsg{Value: "use", Once: true, Priority: 100})
	base := core.Next[string, string](func(_ context.Context, in string) (string, error) { return in, nil })
	if v, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "in", base); err != nil || v != "dynamic<owner<in/dynamic/owner>>" {
		t.Fatal(v, err)
	}
	if e := p.EventsSnapshot(); e.Active != 3 || e.Tokens != 0 {
		t.Fatal("Once Hook resources leaked", e)
	}
	invokeTest(t, h.ctx, endpoint, "AddHook", testMsg{Value: "select", Priority: 100})
	if v, _, err := core.Bind(h.scope).Serial(h.ctx, choice, "select"); err != nil || v != "dynamic" {
		t.Fatal(v, err)
	}
	invokeTest(t, h.ctx, endpoint, "Remove", testMsg{})
	if _, handled, err := core.Bind(h.scope).Serial(h.ctx, choice, "select"); err != nil || !handled {
		t.Fatal(handled, err)
	}
}

func TestBridgeContinuationAdmission(t *testing.T) {
	for _, businessOnly := range []bool{true, false} {
		t.Run(fmt.Sprint("business=", businessOnly), func(t *testing.T) {
			setup := hookSetup(t, "a", false)
			if businessOnly {
				setup.env = append(setup.env, "GORDIS_EV02_LIMIT_ONE=1")
			} else {
				setup.env = append(setup.env, "GORDIS_EV03_CONTINUATION_ONE=1")
			}
			h := newHarness(t, setup)
			p, _, endpoint := h.process("a")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			done := make(chan error, 1)
			go func() {
				_, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "hold", func(context.Context, string) (string, error) { close(entered); <-release; return "ok", nil })
				done <- err
			}()
			if err := waitGate(h.ctx, entered); err != nil {
				t.Fatal(err)
			}
			if d := p.Snapshot(); d.Admission.Business.Pending != 1 || d.Admission.Continuation.Handling != 1 {
				t.Fatal(d)
			}
			// Control is still usable while a Business RPC and its Continue are blocked.
			invokeTest(t, h.ctx, endpoint, "Inspect", testMsg{})
			h.await("subscriptions/3")
			var baseCalled bool
			_, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "rejected", func(context.Context, string) (string, error) { baseCalled = true; return "", nil })
			if !errors.Is(err, rpc.ErrBackpressure) || baseCalled {
				t.Fatal("exhaustion did not reject without executing base", baseCalled, err)
			}
			if p.EventsSnapshot().Tokens != 1 {
				t.Fatal("rejected invocation leaked token", p.EventsSnapshot())
			}
			once.Do(func() { close(release) })
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
		})
	}
}

func TestBridgeChildBaseCancellationAndDrain(t *testing.T) {
	setup := hookSetup(t, "a", false)
	setup.env = append(setup.env, "GORDIS_EV03_BASE_CALLBACK_GATE=1")
	h := newHarness(t, setup)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h.gate = func(ctx context.Context, msg testMsg) error {
		if msg.Value == "base-canceled" {
			<-release
		}
		return nil
	}
	p, _, endpoint := h.process("a")
	c, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := hookCall(c, endpoint, "Waterfall", "base-hold", p.peer.bridge.bindings[transform.ID()])
		done <- err
	}()
	h.await("base-entered")
	cancel()
	h.await("base-canceled")
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	// The Child base and original publisher lease survive caller cancellation.
	invokeTest(t, h.ctx, endpoint, "InspectTokens", testMsg{})
	h.await("tokens/1/1")
	stopped := make(chan error, 1)
	go func() { stopped <- h.host.Shutdown(h.ctx) }()
	eventually(t, h.ctx, func() bool { return p.EventsSnapshot().Closed })
	if p.Snapshot().CallbackHandling == 0 || p.Snapshot().Reaped {
		t.Fatal("cleanup passed blocked Child base callback", p.Snapshot())
	}
	select {
	case err := <-stopped:
		t.Fatal("cleanup passed base", err)
	default:
	}
	unblock()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	d := p.Snapshot()
	if !d.Reaped || !d.CallbacksDrained || d.Cleanup == nil || !d.Cleanup.CleanupComplete {
		t.Fatal(d)
	}
}

func TestBridgeTokenValidationAndSingleUse(t *testing.T) {
	var table tokenTable
	b := hookBridge(t).bindings[transform.ID()]
	id, err := table.add(context.Background(), core.OwnerRef{Instance: "owner", Generation: 1}, b, func(context.Context, any) (any, error) { return "out", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer table.finish(id)
	if _, err := table.invoke(context.Background(), mustRaw(t, continueRequest{id, choice.ID(), json.RawMessage(`"in"`)})); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal(err)
	}
	if out, err := table.invoke(context.Background(), mustRaw(t, continueRequest{id, transform.ID(), json.RawMessage(`"in"`)})); err != nil || out.Result == nil || string(out.Result.Payload) != `"out"` {
		t.Fatal(out, err)
	}
	if _, err := table.invoke(context.Background(), mustRaw(t, continueRequest{id, transform.ID(), json.RawMessage(`"in"`)})); !errors.Is(err, core.ErrNextCalled) {
		t.Fatal(err)
	}
	table.finish(id)
	if _, err := table.invoke(context.Background(), mustRaw(t, continueRequest{id, transform.ID(), json.RawMessage(`"in"`)})); !errors.Is(err, core.ErrNextExpired) {
		t.Fatal(err)
	}
}

func TestBridgeUnsubscribeCancelsContinuation(t *testing.T) {
	h := newHarness(t, hookSetup(t, "a", false))
	p, _, endpoint := h.process("a")
	invokeTest(t, h.ctx, endpoint, "AddHook", testMsg{Value: "use", Priority: 100})
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		_, err := core.Bind(h.scope).Waterfall(h.ctx, transform, "hold", func(ctx context.Context, in string) (string, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return "", ctx.Err()
		})
		done <- err
	}()
	if err := waitGate(h.ctx, entered); err != nil {
		t.Fatal(err)
	}
	invokeTest(t, h.ctx, endpoint, "Remove", testMsg{})
	if err := waitGate(h.ctx, canceled); err != nil {
		t.Fatal(err)
	}
	if e := p.EventsSnapshot(); e.Tokens == 0 || e.Continuing == 0 {
		t.Fatal("unsubscribe lost admitted continuation", e)
	}
	select {
	case err := <-done:
		t.Fatal("unsubscribe passed actual next", err)
	default:
	}
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	if p.EventsSnapshot().Tokens != 0 {
		t.Fatal(p.EventsSnapshot())
	}
	if err := h.host.Shutdown(h.ctx); err != nil {
		t.Fatal(err)
	}
}
