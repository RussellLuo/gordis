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

var notice = core.NewTopic[string]("ev02/notice")
var other = core.NewTopic[string]("ev02/other")
var testHostKey = gordis.NewKey[service]("ev02/host")
var testChildKey = gordis.NewKey[service]("ev02/child")
var testMethods = map[string]rpc.AdmissionClass{"Signal": rpc.Control, "Emit": rpc.Business, "Publish": rpc.Business, "Add": rpc.Control, "Remove": rpc.Control, "Inspect": rpc.Control, "Release": rpc.Control, "Fail": rpc.Control, "Env": rpc.Control, "FailTask": rpc.Control, "EmitEmpty": rpc.Business, "InspectTokens": rpc.Control, "AddHook": rpc.Control, "Serial": rpc.Business, "Waterfall": rpc.Business, "Late": rpc.Business, "EmitFrom": rpc.Business, "PublishFrom": rpc.Business, "WaterfallFrom": rpc.Business, "SerialFrom": rpc.Business, "SecondFrom": rpc.Business, "InternalFrom": rpc.Business}

func testBinding(key gordis.Key[service]) rpc.Binding { return serviceBinding(key, "1", testMethods) }

type testService func(context.Context, string, json.RawMessage) (reply, error)

func (f testService) invoke(ctx context.Context, method string, raw json.RawMessage) (reply, error) {
	return f(ctx, method, raw)
}

type testMsg struct {
	Value, Owner string
	Once, Global bool
	Priority     int
}
type testPlugin struct {
	spec     gordis.PluginSpec
	activate func(context.Context, *gordis.Scope) error
}

func (p *testPlugin) Spec() gordis.PluginSpec {
	s := p.spec
	s.New = func() gordis.Plugin { return &testPlugin{spec: p.spec, activate: p.activate} }
	return s
}
func (p *testPlugin) Activate(ctx context.Context, s *gordis.Scope) error { return p.activate(ctx, s) }
func testBridge(t *testing.T, extra bool) *Bridge {
	t.Helper()
	bindings := []Binding{BindTopic(notice, JSONEventCodec[string]("string/1"), Publish|Subscribe)}
	if extra {
		bindings = append(bindings, BindTopic(other, JSONEventCodec[string]("string/1"), Publish|Subscribe))
	}
	b, err := NewBridge(bindings...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func testIdentity() rpc.Identity {
	return rpc.Identity{ID: "ev02", Version: "1", Protocol: rpc.Protocol}
}
func testBoundary() rpc.Boundary {
	return rpc.Boundary{Imports: []rpc.Binding{testBinding(testHostKey)}, Exports: []rpc.Binding{testBinding(testChildKey)}}
}

// Keep fixture saturation and nested-call capacity explicit, independent of
// the public API's unlimited zero value. Individual tests lower selected fields.
func bridgeFixtureLimits() rpc.Limits {
	return rpc.Limits{
		Business:     rpc.AdmissionLimit{Pending: 16, Handling: 16},
		Control:      rpc.AdmissionLimit{Pending: 8, Handling: 8},
		Continuation: rpc.AdmissionLimit{Pending: 8, Handling: 8},
	}
}

// The actual Child executes the same test binary, including race instrumentation.
func TestBridgeChild(t *testing.T) {
	if os.Getenv("GORDIS_EV02_CHILD") != "1" {
		return
	}
	b := testBridge(t, os.Getenv("GORDIS_EV02_EXTRA") == "1")
	if os.Getenv("GORDIS_EV03_HOOKS") == "1" {
		b = hookBridge(t)
	}
	if os.Getenv("GORDIS_EV02_EVENT_ONLY") == "1" {
		unit, err := b.Unit(rpc.Unit{})
		if err != nil {
			t.Fatal(err)
		}
		if err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: testIdentity(), Unit: unit, Limits: bridgeFixtureLimits()}); err != nil {
			t.Fatal(err)
		}
		return
	}
	var mu sync.Mutex
	var cancels []func()
	release := make(chan struct{})
	var releaseOnce sync.Once
	owners := map[string]*gordis.Scope{}
	var endpoint service
	owner := func(id string, export bool) *testPlugin {
		provides := []gordis.ServiceSpec{}
		if export && os.Getenv("GORDIS_EV03_HOOKS") == "1" {
			provides = append(provides, internalSource.Spec())
		}
		if export {
			provides = append(provides, testChildKey.Spec())
		}
		return &testPlugin{spec: gordis.PluginSpec{ID: id, Requires: []gordis.ServiceSpec{core.BusKey.Spec(), testHostKey.Spec(), childKey.Spec()}, Provides: provides}, activate: func(ctx context.Context, s *gordis.Scope) error {
			var late savedNext
			if os.Getenv("GORDIS_EV03_HOOKS") == "1" {
				if export {
					if err := s.Provide(internalSource, "internal"); err != nil {
						return err
					}
				}
				if err := installHooks(s, id, &late); err != nil {
					return err
				}
			}
			host, err := s.Get(testHostKey)
			if err != nil {
				return err
			}
			signal := func(ctx context.Context, value string) error {
				_, err := call(ctx, host, "Signal", testMsg{Value: value, Owner: id})
				return err
			}
			handler := func(ctx context.Context, msg string) error {
				if err := signal(ctx, "received/"+msg); err != nil {
					return err
				}
				if msg == "request-notice" {
					return core.Bind(s).Publish(ctx, other, "reply")
				}
				if msg == "hold" {
					<-ctx.Done()
					_ = bounded(context.Background(), func(ctx context.Context) error { return signal(ctx, "canceled") })
					<-release
					return ctx.Err()
				}
				if msg == "error" {
					return errors.Join(gordis.ErrStale, errors.New("child handler error"))
				}
				return nil
			}
			cancel, err := core.Bind(s).On(notice, handler)
			if err != nil {
				return err
			}
			mu.Lock()
			owners[id] = s
			cancels = append(cancels, cancel)
			mu.Unlock()
			if os.Getenv("GORDIS_EV02_HUNG_CLEANUP") == "1" {
				if err := s.Defer("blocked business cleanup", func(context.Context) error {
					_ = bounded(context.Background(), func(ctx context.Context) error { return signal(ctx, "cleanup-entered") })
					select {}
				}); err != nil {
					return err
				}
			}
			if os.Getenv("GORDIS_EV02_INIT_FAIL") == "1" {
				return errors.New("initialization failure after subscription")
			}
			if export {
				endpoint = testService(func(ctx context.Context, method string, raw json.RawMessage) (reply, error) {
					var msg testMsg
					if err := json.Unmarshal(raw, &msg); err != nil {
						return reply{}, err
					}
					switch method {
					case "InspectTokens":
						peer, err := s.Get(childKey)
						if err != nil {
							return reply{}, err
						}
						total, running := peer.(*childPeer).tokens.snapshot()
						return reply{}, signal(ctx, fmt.Sprintf("tokens/%d/%d", total, running))
					case "AddHook":
						opts := onOptions(core.RemoteOptions{Once: msg.Once, Global: msg.Global, Priority: msg.Priority})
						var remove func()
						var err error
						if msg.Value == "select" {
							remove, err = core.Bind(s).Handle(choice, func(context.Context, string) (string, bool, error) { return "dynamic", true, nil }, opts...)
						} else {
							remove, err = core.Bind(s).Use(transform, func(ctx context.Context, in string, next core.Next[string, string]) (string, error) {
								v, err := next(ctx, in+"/dynamic")
								return "dynamic<" + v + ">", err
							}, opts...)
						}
						if err == nil {
							mu.Lock()
							cancels = append(cancels, remove)
							mu.Unlock()
						}
						return reply{}, err
					case "Serial", "SerialFrom":
						var v string
						var handled bool
						var err error
						if method == "SerialFrom" {
							v, handled, err = core.Bind(s).SerialFrom(ctx, testChildKey, choice, msg.Value)
						} else {
							v, handled, err = core.Bind(s).Serial(ctx, choice, msg.Value)
						}
						return resultReply(b.bindings[choice.ID()], core.RemoteResult{Value: v, Handled: handled}, err)
					case "Waterfall", "WaterfallFrom":
						base := core.Next[string, string](func(ctx context.Context, in string) (string, error) {
							if msg.Value == "base-panic" {
								panic("child base panic")
							}
							if in == "base-hold" {
								_ = signal(ctx, "base-entered")
								<-ctx.Done()
								_ = bounded(context.Background(), func(c context.Context) error { return signal(c, "base-canceled") })
								if os.Getenv("GORDIS_EV03_BASE_CALLBACK_GATE") != "1" {
									<-release
								}
								return "", ctx.Err()
							}
							return "base(" + in + ")", nil
						})
						var v string
						var err error
						if method == "WaterfallFrom" {
							v, err = core.Bind(s).WaterfallFrom(ctx, testChildKey, transform, msg.Value, base)
						} else {
							v, err = core.Bind(s).Waterfall(ctx, transform, msg.Value, base)
						}
						return resultReply(b.bindings[transform.ID()], core.RemoteResult{Value: v, Handled: err == nil}, err)
					case "Late":
						v, err := late.call(ctx, msg.Value)
						return resultReply(b.bindings[transform.ID()], core.RemoteResult{Value: v, Handled: err == nil}, err)
					case "PublishFrom":
						return reply{}, core.Bind(s).PublishFrom(ctx, testChildKey, notice, msg.Value)
					case "EmitFrom":
						return reply{}, core.Bind(s).EmitFrom(ctx, testChildKey, notice, msg.Value)
					case "InternalFrom":
						return reply{}, core.Bind(s).EmitFrom(ctx, internalSource, notice, msg.Value)
					case "SecondFrom":
						mu.Lock()
						second := owners["second"]
						mu.Unlock()
						return reply{}, core.Bind(second).EmitFrom(ctx, testChildKey, notice, msg.Value)
					case "EmitEmpty":
						return reply{}, core.Bind(s).Emit(ctx, other, msg.Value)
					case "FailTask":
						return reply{}, s.Go("failed business", func(context.Context) error { return errors.New("business task failed") })
					case "Emit":
						return reply{}, core.Bind(s).Emit(ctx, notice, msg.Value)
					case "Publish":
						return reply{}, core.Bind(s).Publish(ctx, notice, msg.Value)
					case "Add":
						opts := []core.OnOption{core.Priority(msg.Priority)}
						if msg.Once {
							opts = append(opts, core.Once())
						}
						if msg.Global {
							opts = append(opts, core.Global())
						}
						remove, err := core.Bind(s).On(notice, func(ctx context.Context, value string) error { return signal(ctx, "dynamic/"+value) }, opts...)
						if err == nil {
							mu.Lock()
							cancels = append(cancels, remove)
							mu.Unlock()
						}
						return reply{}, err
					case "Remove":
						mu.Lock()
						all := append([]func(){}, cancels...)
						cancels = nil
						mu.Unlock()
						for _, cancel := range all {
							cancel()
						}
						return reply{}, nil
					case "Inspect":
						peer, err := s.Get(childKey)
						if err != nil {
							return reply{}, err
						}
						remote := peer.(*childPeer)
						remote.mu.Lock()
						count := len(remote.subs)
						remote.mu.Unlock()
						return reply{}, signal(ctx, fmt.Sprintf("subscriptions/%d", count))
					case "Release":
						releaseOnce.Do(func() { close(release) })
						return reply{}, nil
					case "Fail":
						os.Exit(23)
					case "Env":
						_, err := s.Env().MountReady(ctx, gordis.InstanceSpec{ID: "forged-host", Plugin: "observer"})
						if err == nil {
							return reply{}, errors.New("Child mounted a Host prototype")
						}
						return reply{}, nil
					}
					return reply{}, ErrInvalidRequest
				})
				if err := s.Provide(testChildKey, endpoint); err != nil {
					return err
				}
			}
			if os.Getenv("GORDIS_EV02_AFTER_READY") == "1" && export {
				return s.AfterReady("self publish", func(ctx context.Context) error {
					if err := core.Bind(s).Emit(ctx, notice, "self"); err != nil {
						return err
					}
					return signal(ctx, "self-complete")
				})
			}
			return nil
		}}
	}
	plugins := []gordis.Plugin{owner("owner", true)}
	instances := []gordis.InstanceSpec{{ID: "owner", Plugin: "owner"}}
	if os.Getenv("GORDIS_EV02_MULTI_OWNER") == "1" {
		plugins = append(plugins, owner("second", false))
		instances = append(instances, gordis.InstanceSpec{ID: "second", Plugin: "second"})
	}
	unit, err := b.Unit(rpc.Unit{Plugins: plugins, Instances: instances, Imports: testBoundary().Imports, Exports: []rpc.Export{{Instance: "owner", Binding: testBinding(testChildKey)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: testIdentity(), Unit: unit, Limits: bridgeFixtureLimits()}); err != nil {
		t.Fatal(err)
	}
}

type capturedProcess struct {
	prototype *ProcessPlugin
	opened    chan *ProcessPlugin
	actual    *ProcessPlugin
}

func (p *capturedProcess) Spec() gordis.PluginSpec {
	if p.actual != nil {
		return p.actual.Spec()
	}
	s := p.prototype.Spec()
	newPlugin := s.New
	s.New = func() gordis.Plugin {
		actual := newPlugin().(*ProcessPlugin)
		return &capturedProcess{actual: actual, opened: p.opened}
	}
	return s
}
func (p *capturedProcess) Activate(ctx context.Context, s *gordis.Scope) error {
	err := p.actual.Activate(ctx, s)
	p.opened <- p.actual
	return err
}

type processSetup struct {
	id     string
	bridge *Bridge
	env    []string
}

type bridgeHarness struct {
	t         *testing.T
	ctx       context.Context
	host      *gordis.Host
	scope     *gordis.Scope
	mu        sync.Mutex
	messages  []testMsg
	signals   chan testMsg
	gate      func(context.Context, testMsg) error
	opened    map[string]chan *ProcessPlugin
	endpoints map[string]service
	labels    map[string]string
	partition *gordis.Scope
}

func newHarness(t *testing.T, setups ...processSetup) *bridgeHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	h := &bridgeHarness{t: t, ctx: ctx, signals: make(chan testMsg, 10000), opened: map[string]chan *ProcessPlugin{}, endpoints: map[string]service{}, labels: map[string]string{}}
	observer := &testPlugin{spec: gordis.PluginSpec{ID: "observer", Requires: []gordis.ServiceSpec{core.BusKey.Spec()}, Provides: []gordis.ServiceSpec{testHostKey.Spec()}}, activate: func(_ context.Context, s *gordis.Scope) error {
		h.scope = s
		callback := testService(func(ctx context.Context, _ string, raw json.RawMessage) (reply, error) {
			var msg testMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				return reply{}, err
			}
			h.record(msg)
			if h.gate != nil {
				return reply{}, h.gate(ctx, msg)
			}
			return reply{}, nil
		})
		if err := s.Provide(testHostKey, service(callback)); err != nil {
			return err
		}
		_, err := core.Bind(s).On(notice, func(ctx context.Context, msg string) error {
			h.record(testMsg{Owner: "host", Value: "received/" + msg})
			return nil
		})
		return err
	}}
	partition := &testPlugin{spec: gordis.PluginSpec{ID: "partition", Requires: []gordis.ServiceSpec{core.BusKey.Spec()}}, activate: func(_ context.Context, s *gordis.Scope) error {
		h.partition = s
		_, err := core.Bind(s).On(notice, func(_ context.Context, msg string) error {
			h.record(testMsg{Owner: "partition", Value: "received/" + msg})
			return nil
		})
		return err
	}}
	plugins := []gordis.Plugin{&core.Plugin{}, observer, partition}
	if len(setups) == 0 {
		setups = []processSetup{{id: "a", bridge: testBridge(t, false)}}
	}
	for _, setup := range setups {
		limits := bridgeFixtureLimits()
		limits.StartupTimeout, limits.StopTimeout = 15*time.Second, 5*time.Second
		p, err := setup.bridge.NewProcess(rpc.ProcessOptions{PluginID: setup.id, Command: rpc.Command{Path: os.Args[0], Args: []string{"-test.run=^TestBridgeChild$"}, Env: append([]string{"GORDIS_EV02_CHILD=1"}, setup.env...)}, Identity: testIdentity(), ChildBoundary: testBoundary(), Limits: limits})
		if err != nil {
			t.Fatal(err)
		}
		for _, env := range setup.env {
			if env == "GORDIS_EV03_CONTINUATION_ONE=1" {
				p.options.Limits.Continuation = rpc.AdmissionLimit{Pending: 1, Handling: 1}
			}
			if env == "GORDIS_EV02_LIMIT_ONE=1" {
				p.options.Limits.Business = rpc.AdmissionLimit{Pending: 1, Handling: 1}
			}
		}
		opened := make(chan *ProcessPlugin, 10)
		h.opened[setup.id] = opened
		plugins = append(plugins, &capturedProcess{prototype: p, opened: opened})
		id := setup.id
		plugins = append(plugins, &testPlugin{spec: gordis.PluginSpec{ID: id + "/consumer", Requires: []gordis.ServiceSpec{testChildKey.Spec()}}, activate: func(_ context.Context, s *gordis.Scope) error {
			endpoint, err := s.Get(testChildKey)
			h.endpoints[id] = endpoint
			return err
		}})
	}
	var err error
	h.host, err = gordis.NewHost(plugins...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.host.Shutdown(c)
	})
	for _, id := range []string{core.PluginID, "observer"} {
		if _, err = h.host.Env().MountReady(ctx, gordis.InstanceSpec{ID: id, Plugin: id}); err != nil {
			t.Fatal(err)
		}
	}
	return h
}
func (h *bridgeHarness) record(msg testMsg) {
	h.mu.Lock()
	h.messages = append(h.messages, msg)
	h.mu.Unlock()
	h.signals <- msg
}
func (h *bridgeHarness) count(owner, value string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, msg := range h.messages {
		if msg.Owner == owner && msg.Value == value {
			n++
		}
	}
	return n
}
func (h *bridgeHarness) await(value string) {
	h.t.Helper()
	for {
		select {
		case msg := <-h.signals:
			if msg.Value == value {
				return
			}
		case <-h.ctx.Done():
			h.t.Fatal("missing", value, h.ctx.Err())
		}
	}
}
func (h *bridgeHarness) process(id string) (*ProcessPlugin, *gordis.Instance, service) {
	h.t.Helper()
	e := h.host.Env().Isolate(testChildKey, id)
	if label := h.labels[id]; label != "" {
		e = e.Isolate(notice, label)
	}
	instance, err := e.MountReady(h.ctx, gordis.InstanceSpec{ID: id, Plugin: id})
	if err != nil {
		h.t.Fatal(err)
	}
	actual := <-h.opened[id]
	if _, err = e.MountReady(h.ctx, gordis.InstanceSpec{ID: id + "-consumer", Plugin: id + "/consumer"}); err != nil {
		h.t.Fatal(err)
	}
	return actual, instance, h.endpoints[id]
}
func invokeTest(t *testing.T, ctx context.Context, endpoint service, method string, msg testMsg) {
	t.Helper()
	if _, err := call(ctx, endpoint, method, msg); err != nil {
		t.Fatal(method, err)
	}
}

func TestBridgeExecutionCombinations(t *testing.T) {
	h := newHarness(t, processSetup{"a", testBridge(t, false), []string{"GORDIS_EV02_AFTER_READY=1", "GORDIS_EV02_MULTI_OWNER=1"}}, processSetup{"b", testBridge(t, true), []string{"GORDIS_EV02_EXTRA=1"}})
	a, _, endpoint := h.process("a")
	h.await("self-complete")
	if h.count("host", "received/self") != 1 || h.count("owner", "received/self") != 1 || h.count("second", "received/self") != 1 {
		t.Fatal("self publish did not traverse Host exactly once", h.messages)
	}
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "host"); err != nil {
		t.Fatal(err)
	}
	invokeTest(t, h.ctx, endpoint, "Emit", testMsg{Value: "child"})
	for _, owner := range []string{"host", "owner", "second"} {
		for _, msg := range []string{"host", "child"} {
			if h.count(owner, "received/"+msg) != 1 {
				t.Fatal("missing execution", owner, msg)
			}
		}
	}
	// Same View and another registry coexist. No private endpoint provider conflict.
	c, _, second := h.process("b")
	invokeTest(t, h.ctx, second, "EmitEmpty", testMsg{Value: "empty"})
	invokeTest(t, h.ctx, second, "Publish", testMsg{Value: "cross"})
	if h.count("owner", "received/cross") != 2 || h.count("second", "received/cross") != 1 {
		t.Fatal("Child to Child routing")
	}
	if a.EventsSnapshot().Active != 2 || c.EventsSnapshot().Active != 1 {
		t.Fatal(a.EventsSnapshot(), c.EventsSnapshot())
	}
	if err := h.host.Shutdown(h.ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*ProcessPlugin{a, c} {
		d := p.Snapshot()
		e := p.EventsSnapshot()
		if !d.Reaped || d.Cleanup == nil || !d.Cleanup.CleanupComplete || !d.CallbacksDrained || e.Active+e.Pending+e.Committed+e.Dispatching+e.Delivering != 0 {
			t.Fatalf("incomplete cleanup: %+v %+v", d, e)
		}
	}
}
func TestBridgeNotificationOptionsAndErrors(t *testing.T) {
	h := newHarness(t)
	p, _, endpoint := h.process("a")
	invokeTest(t, h.ctx, endpoint, "Add", testMsg{Once: true, Priority: 10, Global: true})
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "ordered"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	indexDynamic, indexOriginal := -1, -1
	for i, msg := range h.messages {
		if msg.Value == "dynamic/ordered" {
			indexDynamic = i
		}
		if msg.Owner == "owner" && msg.Value == "received/ordered" {
			indexOriginal = i
		}
	}
	h.mu.Unlock()
	if indexDynamic < 0 || indexOriginal < indexDynamic {
		t.Fatal("priority order")
	}
	if p.EventsSnapshot().Active != 1 {
		t.Fatal("Once Host mapping leaked", p.EventsSnapshot())
	}
	invokeTest(t, h.ctx, endpoint, "Inspect", testMsg{})
	h.await("subscriptions/1")
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	invokeTest(t, h.ctx, endpoint, "Add", testMsg{Once: true})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- core.Bind(h.scope).Publish(h.ctx, notice, "once") }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if h.count("owner", "dynamic/once") != 1 {
		t.Fatal("Once admitted more than once")
	}
	_, err := core.Bind(h.scope).On(notice, func(_ context.Context, msg string) error {
		if msg == "error" {
			h.record(testMsg{Owner: "tail", Value: "error"})
			return context.DeadlineExceeded
		}
		return nil
	}, core.Priority(-100))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"Emit", "Publish"} {
		_, err := call(h.ctx, endpoint, method, testMsg{Value: "error"})
		if !errors.Is(err, gordis.ErrStale) {
			t.Fatal("lost handler sentinel", err)
		}
		if errors.Is(err, context.DeadlineExceeded) != (method == "Publish") {
			t.Fatal("Emit first error / Publish aggregate", method, err)
		}
		expected := 0
		if method == "Publish" {
			expected = 1
		}
		if h.count("tail", "error") != expected {
			t.Fatal("Emit failed to stop at first error")
		}
	}

	invokeTest(t, h.ctx, endpoint, "Remove", testMsg{})
	if p.EventsSnapshot().Active != 0 {
		t.Fatal("unsubscribe leaked")
	}
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "removed"); err != nil {
		t.Fatal(err)
	}
	if h.count("owner", "received/removed") != 0 {
		t.Fatal("removed handler called")
	}
}

// These wrappers only pause production Binding handlers/clients. They do not
// implement subscription, Ready, budget or cleanup state machines themselves.
func interceptHost(b *Bridge, fn func(context.Context, string, json.RawMessage, service) (reply, error)) {
	b.host = rpc.NewBinding(hostKey, b.host.Version(), rpc.Codec[service]{MethodClasses: hostMethods, Client: func(inv rpc.Invoker) service { return &client{inv} }, Server: func(peer service) rpc.Handler {
		h := rpc.Handler{Methods: map[string]rpc.Unary{}}
		for method := range hostMethods {
			h.Methods[method] = func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				out, err := fn(ctx, method, raw, peer)
				if err != nil {
					out = errorReply(err)
				}
				return json.Marshal(out)
			}
		}
		return h
	}})
}
func waitGate(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestBridgeReadyGates(t *testing.T) {
	b := testBridge(t, false)
	subscribed, subscribeRelease := make(chan struct{}), make(chan struct{})
	ownerEntered, ownerProcess := make(chan struct{}), make(chan struct{})
	ownerProcessed, ownerAck := make(chan struct{}), make(chan struct{})
	enableEntered, enableRelease, enabled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		for _, ch := range []chan struct{}{subscribeRelease, ownerProcess, ownerAck, enableRelease} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	}()
	interceptHost(b, func(ctx context.Context, method string, raw json.RawMessage, peer service) (reply, error) {
		if method == "OwnerReady" {
			close(ownerEntered)
			if err := waitGate(ctx, ownerProcess); err != nil {
				return reply{}, err
			}
		}
		out, err := peer.invoke(ctx, method, raw)
		if method == "Subscribe" {
			close(subscribed)
			if e := waitGate(ctx, subscribeRelease); e != nil {
				return reply{}, e
			}
		}
		if method == "OwnerReady" {
			close(ownerProcessed)
			if e := waitGate(ctx, ownerAck); e != nil {
				return reply{}, e
			}
		}
		return out, err
	})
	original := b.child
	b.child = rpc.NewBinding(childKey, original.Version(), rpc.Codec[service]{MethodClasses: childMethods, Server: func(service) rpc.Handler { return rpc.Handler{} }, Client: func(inv rpc.Invoker) service {
		next := &client{inv}
		return testService(func(ctx context.Context, method string, raw json.RawMessage) (reply, error) {
			if method == "Enable" {
				close(enableEntered)
				if err := waitGate(ctx, enableRelease); err != nil {
					return reply{}, err
				}
			}
			out, err := next.invoke(ctx, method, raw)
			if method == "Enable" {
				close(enabled)
			}
			return out, err
		})
	}})
	h := newHarness(t, processSetup{"a", b, []string{"GORDIS_EV02_AFTER_READY=1"}})
	done := make(chan error, 1)
	go func() {
		_, err := h.host.Env().Isolate(testChildKey, "a").MountReady(h.ctx, gordis.InstanceSpec{ID: "a", Plugin: "a"})
		done <- err
	}()
	await := func(ch <-chan struct{}) {
		t.Helper()
		if err := waitGate(h.ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	await(subscribed)
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "pending"); err != nil {
		t.Fatal(err)
	}
	if h.count("owner", "received/pending") != 0 {
		t.Fatal("pending subscription exposed")
	}
	close(subscribeRelease)
	await(ownerEntered)
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "committed"); err != nil {
		t.Fatal(err)
	}
	if h.count("owner", "received/committed") != 0 {
		t.Fatal("owner not Ready")
	}
	close(ownerProcess)
	await(ownerProcessed)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	await(enableEntered)
	if h.count("host", "received/self") != 0 {
		t.Fatal("Child bypassed Enable")
	}
	if err := core.Bind(h.scope).Emit(h.ctx, notice, "ready"); err != nil {
		t.Fatal(err)
	}
	if h.count("owner", "received/ready") != 1 {
		t.Fatal("MountReady did not install initial subscriptions")
	}
	close(enableRelease)
	await(enabled)
	if h.count("host", "received/self") != 0 {
		t.Fatal("Child bypassed its OwnerReady acknowledgment")
	}
	close(ownerAck)
	h.await("self-complete")
}

func TestBridgeDynamicCommitAndRollback(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint("lost=", lost), func(t *testing.T) {
			b := testBridge(t, false)
			entered, release := make(chan struct{}), make(chan struct{})
			var count int
			var once sync.Once
			interceptHost(b, func(ctx context.Context, method string, raw json.RawMessage, peer service) (reply, error) {
				out, err := peer.invoke(ctx, method, raw)
				if method == "Subscribe" {
					count++
					if count == 2 {
						close(entered)
						if e := waitGate(ctx, release); e != nil {
							return reply{}, e
						}
					}
				}
				if method == "CommitSubscription" && lost {
					var msg idRequest
					_ = json.Unmarshal(raw, &msg)
					if msg.ID == 2 {
						return reply{}, context.DeadlineExceeded
					}
				}
				if method == "CommitSubscription" && !lost {
					var msg idRequest
					_ = json.Unmarshal(raw, &msg)
					if msg.ID == 2 {
						if e := core.Bind(peer.(*hostPeer).scope).Emit(ctx, notice, "during-commit"); e != nil {
							return reply{}, e
						}
					}
				}
				return out, err
			})
			h := newHarness(t, processSetup{"a", b, nil})
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			p, _, endpoint := h.process("a")
			done := make(chan error, 1)
			go func() { _, err := call(h.ctx, endpoint, "Add", testMsg{}); done <- err }()
			if err := waitGate(h.ctx, entered); err != nil {
				t.Fatal(err)
			}
			if p.EventsSnapshot().Pending != 1 {
				t.Fatal("dynamic Subscribe did not remain pending")
			}
			if err := core.Bind(h.scope).Emit(h.ctx, notice, "window"); err != nil {
				t.Fatal(err)
			}
			if h.count("owner", "dynamic/window") != 0 {
				t.Fatal("delivered before Child install")
			}
			once.Do(func() { close(release) })
			select {
			case err := <-done:
				if lost != errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("lost Commit response", err)
				}
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			expected := 2
			if lost {
				expected = 1
			}
			if p.EventsSnapshot().Active != expected || p.EventsSnapshot().Pending != 0 {
				t.Fatal("Commit cleanup", p.EventsSnapshot())
			}
			invokeTest(t, h.ctx, endpoint, "Inspect", testMsg{})
			h.await(fmt.Sprintf("subscriptions/%d", expected))
			if err := core.Bind(h.scope).Emit(h.ctx, notice, "installed"); err != nil {
				t.Fatal(err)
			}
			if !lost && h.count("owner", "dynamic/installed") != 1 {
				t.Fatal("dynamic Commit did not activate")
			}
			if !lost && h.count("owner", "dynamic/during-commit") != 1 {
				t.Fatal("handler unavailable before Commit response")
			}
		})
	}
}
