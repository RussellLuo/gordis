package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type declarationPlugin struct {
	spec  gordis.PluginSpec
	value string
}

func (p *declarationPlugin) Spec() gordis.PluginSpec { return p.spec }
func (p *declarationPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide(gordis.NewKey[string]("test/value"), p.value)
}
func valuePlugin() *declarationPlugin {
	p := &declarationPlugin{value: "value"}
	p.spec = gordis.PluginSpec{ID: "value", New: func() gordis.Plugin { return valuePlugin() }, Provides: []gordis.ServiceSpec{gordis.NewKey[string]("test/value").Spec()}}
	return p
}
func valueBinding(validate func(context.Context, string) error) Binding {
	return NewBinding(gordis.NewKey[string]("test/value"), "1", Codec[string]{
		Client: func(Invoker) string { return "client" },
		Server: func(value string) Handler {
			return Handler{Methods: map[string]Unary{"get": func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.Marshal(value) }}}
		},
		Validate: validate,
	})
}
func TestUnitRejectsInvalidBoundaryAndGraph(t *testing.T) {
	binding := valueBinding(nil)
	for _, tc := range []struct {
		name   string
		change func(*Unit)
	}{
		{"missing", func(u *Unit) { u.Instances = nil }},
		{"duplicate-instance", func(u *Unit) { u.Instances = append(u.Instances, u.Instances[0]) }},
		{"duplicate-provider", func(u *Unit) { u.Imports = []Binding{binding} }},
		{"wrong-type", func(u *Unit) {
			u.Plugins[0] = &declarationPlugin{spec: gordis.PluginSpec{ID: "value", Provides: []gordis.ServiceSpec{gordis.NewKey[int]("test/value").Spec()}}}
		}},
		{"unknown-source", func(u *Unit) { u.Exports[0].Instance = "missing" }},
		{"incomplete-binding", func(u *Unit) { u.Exports[0].Binding = Binding{} }},
		{"duplicate-export", func(u *Unit) { u.Exports = append(u.Exports, u.Exports[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit := Unit{Plugins: []gordis.Plugin{valuePlugin()}, Instances: []gordis.InstanceSpec{{ID: "value", Plugin: "value"}}, Exports: []Export{{Instance: "value", Binding: binding}}}
			if err := unit.validate(); err != nil {
				t.Fatal(err)
			}
			tc.change(&unit)
			if err := unit.validate(); err == nil {
				t.Fatal("invalid Unit accepted")
			}
		})
	}
}
func TestExportValidationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	binding := valueBinding(func(ctx context.Context, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() })
	unit := Unit{Plugins: []gordis.Plugin{valuePlugin()}, Instances: []gordis.InstanceSpec{{ID: "value", Plugin: "value"}}, Exports: []Export{{Instance: "value", Binding: binding}}}
	done := make(chan error, 1)
	go func() {
		host, _, err := assemble(ctx, unit, nil, nil)
		if host != nil {
			_ = host.Shutdown(context.Background())
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("validation did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("validation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("validation ignored cancellation")
	}
}

func TestCallbackServerCleanupDuringInstallation(t *testing.T) {
	// Stop before/after/concurrently with broker construction. A stopped gRPC
	// server must reject Serve even when it is installed after cleanup.
	for _, order := range []string{"before", "after", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			callbacks := new(callbackServer)
			server := grpc.NewServer()
			switch order {
			case "before":
				callbacks.stop()
				callbacks.install(server)
			case "after":
				callbacks.install(server)
				callbacks.stop()
			case "concurrent":
				var workers sync.WaitGroup
				workers.Add(2)
				go func() { defer workers.Done(); callbacks.install(server) }()
				go func() { defer workers.Done(); callbacks.stop() }()
				workers.Wait()
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err = server.Serve(listener); !errors.Is(err, grpc.ErrServerStopped) {
				t.Fatalf("late server still serves: %v", err)
			}
		})
	}
}

type importValidationLoader struct {
	binding Binding
	result  chan error
}

func (p *importValidationLoader) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "loader", New: func() gordis.Plugin { return &importValidationLoader{p.binding, p.result} }, Requires: []gordis.ServiceSpec{p.binding.Spec()}}
}
func (p *importValidationLoader) Activate(ctx context.Context, scope *gordis.Scope) error {
	_, err := Attach(ctx, scope, AttachOptions{
		Command: Command{Path: "/must-not-start"}, Identity: Identity{ID: "test", Version: "1", Protocol: Protocol},
		ChildBoundary: Boundary{Imports: []Binding{p.binding}}, Limits: Limits{StartupTimeout: 50 * time.Millisecond},
	})
	p.result <- err
	return err
}
func TestImportValidationUsesStartupDeadline(t *testing.T) {
	result := make(chan error, 1)
	binding := valueBinding(func(ctx context.Context, _ string) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("missing startup deadline")
		}
		<-ctx.Done()
		return ctx.Err()
	})
	host, err := gordis.NewHost(valuePlugin(), &importValidationLoader{binding, result})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer host.Shutdown(ctx)
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "value", Plugin: "value"}); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "loader", Plugin: "loader"}); err == nil {
		t.Fatal("blocked validation became Ready")
	}
	select {
	case err = <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("startup deadline: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("import validation ignored startup timeout")
	}
}

type testPlugin struct {
	spec     gordis.PluginSpec
	activate func(context.Context, *gordis.Scope) error
}

func (p *testPlugin) Spec() gordis.PluginSpec {
	spec := p.spec
	spec.New = func() gordis.Plugin { return &testPlugin{p.spec, p.activate} }
	return spec
}
func (p *testPlugin) Activate(ctx context.Context, s *gordis.Scope) error { return p.activate(ctx, s) }
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func receiveResult[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}
func assertNotCompleted(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("completed before release: %v", err)
	default:
	}
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s contextServerStream) Context() context.Context { return s.ctx }
func TestCallbackStreamDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callCtx, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	tracker := new(callbackServer)
	session := &Session{callbacks: tracker}
	if d := session.Snapshot(); d.CallbacksClosed || d.CallbacksDrained || d.CallbackTransportClosed || d.CallbackHandling != 0 {
		t.Fatalf("idle callback server reported drained: %+v", d)
	}
	info := SessionInfo{ID: "stream", HostInstance: "host", HostGeneration: 1}
	handler := Handler{Streams: map[string]Stream{"hold": func(ctx context.Context, _ json.RawMessage, _ func(json.RawMessage) error) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}}}
	router := &callbackRouter{admission: newAdmission(Limits{}), info: info, tracker: tracker, handlers: map[string]boundHandler{"callback": {Handler: handler}}}
	packet, err := encode(packet{Session: info, Contract: "callback", Method: "hold", Payload: json.RawMessage(`{}`)})
	requireNoError(t, err)
	returned := make(chan error, 1)
	go func() { returned <- router.stream("Stream", packet, contextServerStream{ctx: callCtx}) }()
	receiveResult(t, ctx, entered)
	cancelCall()
	receiveResult(t, ctx, canceled)
	if d := session.Snapshot(); d.CallbackHandling != 1 || d.CallbacksDrained {
		t.Fatalf("lost canceled stream handler: %+v", d)
	}
	stopped := make(chan error, 1)
	go func() { tracker.stop(); stopped <- nil }()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if session.Snapshot().CallbacksClosed {
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if d := session.Snapshot(); d.CallbackHandling != 1 || d.CallbacksDrained || d.CallbackTransportClosed {
		t.Fatalf("blocked stream reported drained: %+v", d)
	}
	assertNotCompleted(t, stopped)
	if _, err = tracker.enter(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal("admitted callback after closure")
	}
	unblock()
	receiveResult(t, ctx, returned)
	requireNoError(t, receiveResult(t, ctx, stopped))
	if d := session.Snapshot(); !d.CallbacksClosed || !d.CallbacksDrained || !d.CallbackTransportClosed || d.CallbackHandling != 0 {
		t.Fatalf("stream not drained: %+v", d)
	}
}
func TestCallbackSessionIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	handler := Handler{Methods: map[string]Unary{"call": func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`"ok"`), nil
	}}, Streams: map[string]Stream{"stream": func(context.Context, json.RawMessage, func(json.RawMessage) error) error { calls.Add(1); return nil }}}
	info := SessionInfo{"current", "owner", 3}
	router := &callbackRouter{info: info, admission: newAdmission(Limits{}), tracker: new(callbackServer), handlers: map[string]boundHandler{"declared": {Handler: handler}}}
	for _, bad := range []SessionInfo{{"foreign", info.HostInstance, info.HostGeneration}, {info.ID, "foreign", info.HostGeneration}, {info.ID, info.HostInstance, 2}} {
		in, err := encode(packet{Session: bad, Contract: "declared", Method: "call"})
		requireNoError(t, err)
		if _, err := router.unary(ctx, "Call", in); err == nil {
			t.Fatal("foreign callback accepted")
		}
		if err := router.stream("Stream", in, contextServerStream{ctx: ctx}); err == nil {
			t.Fatal("foreign callback stream accepted")
		}
	}
	for _, contract := range []string{"undeclared", testExportKey.Name()} {
		in, _ := encode(packet{Session: info, Contract: contract, Method: "call"})
		if _, err := router.unary(ctx, "Call", in); status.Code(err) != codes.PermissionDenied {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected packet entered a callback")
	}
	in, _ := encode(packet{Session: info, Contract: "declared", Method: "call"})
	_, err := router.unary(ctx, "Call", in)
	requireNoError(t, err)
	in, _ = encode(packet{Session: info, Contract: "declared", Method: "stream"})
	requireNoError(t, router.stream("Stream", in, contextServerStream{ctx: ctx}))
	router.tracker.stop()
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	if d := router.admission.snapshot(); d.Business.Handling != 0 {
		t.Fatalf("rejected packets leaked capacity: %+v", d)
	}
}

func TestCallbackHandlerPanicReleasesAdmission(t *testing.T) {
	info := SessionInfo{ID: "panic", HostInstance: "owner", HostGeneration: 1}
	handler := Handler{
		Methods: map[string]Unary{"call": func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			if string(raw) == `"panic"` {
				panic("handler failure")
			}
			return raw, nil
		}},
		Streams: map[string]Stream{"stream": func(_ context.Context, raw json.RawMessage, _ func(json.RawMessage) error) error {
			if string(raw) == `"panic"` {
				panic("handler failure")
			}
			return nil
		}},
	}
	router := &callbackRouter{info: info, admission: newAdmission(singleCallLimits()), tracker: new(callbackServer), handlers: map[string]boundHandler{"service": {Handler: handler}}}
	for _, stream := range []bool{false, true} {
		for _, payload := range []string{`"panic"`, `"healthy"`} {
			method := "call"
			if stream {
				method = "stream"
			}
			in, err := encode(packet{Session: info, Contract: "service", Method: method, Payload: json.RawMessage(payload)})
			requireNoError(t, err)
			if stream {
				err = router.stream("Stream", in, contextServerStream{ctx: context.Background()})
			} else {
				_, err = router.unary(context.Background(), "Call", in)
			}
			if payload == `"panic"` {
				if status.Code(err) != codes.Internal {
					t.Fatalf("panic: %v", err)
				}
			} else {
				requireNoError(t, err)
			}
			_, handling, _ := router.tracker.snapshot()
			if handling != 0 || router.admission.snapshot() != (AdmissionSnapshot{}) {
				t.Fatal("handler exit leaked callback or admission count")
			}
		}
	}
}
func TestUnitAdmissionBeforeStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var scope *gordis.Scope
	p := &testPlugin{spec: gordis.PluginSpec{ID: "owner"}, activate: func(_ context.Context, s *gordis.Scope) error { scope = s; return nil }}
	host, err := gordis.NewHost(p)
	requireNoError(t, err)
	defer host.Shutdown(context.Background())
	_, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "owner", Plugin: "owner"})
	requireNoError(t, err)
	var calls atomic.Int32
	h := boundHandler{scope: scope, Handler: Handler{Methods: map[string]Unary{"work": func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{}`), nil
	}}}}
	domain, cancelDomain := context.WithCancel(ctx)
	defer cancelDomain()
	info := SessionInfo{ID: "receive", HostInstance: "owner", HostGeneration: 1}
	server := &unitServer{info: info, ready: true, initialized: true, domain: domain, cancel: cancelDomain, host: host, admission: newAdmission(singleCallLimits()), handlers: map[string]boundHandler{"service": h}, done: make(chan struct{})}
	packet := packet{Session: info, Contract: "service", Method: "work", Payload: json.RawMessage(`{}`)}
	canceled, cancelEarly := context.WithCancel(ctx)
	cancelEarly()
	if _, _, _, err = server.acquire(canceled, packet, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if server.admission.snapshot() != (AdmissionSnapshot{}) {
		t.Fatal("pre-canceled request was counted")
	}
	// Pause exactly between receive admission/Scope acquisition and user dispatch.
	bound, handlerCtx, release, err := server.acquire(ctx, packet, false)
	requireNoError(t, err)
	var once sync.Once
	done := func() { once.Do(release) }
	defer done()
	server.beginStop()
	receiveResult(t, ctx, handlerCtx.Done())
	if _, _, _, err = server.acquire(ctx, packet, false); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	select {
	case <-server.done:
		t.Fatal("cleanup escaped admitted request")
	default:
	}
	if server.admission.snapshot().Business.Handling != 1 {
		t.Fatal("admitted request escaped drain")
	}
	if _, err = call(handlerCtx, bound.Handler, packet); !errors.Is(contextError(err), context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("canceled work entered user handler")
	}
	done()
	receiveResult(t, ctx, server.done)
	if server.admission.snapshot() != (AdmissionSnapshot{}) {
		t.Fatal(server.admission.snapshot())
	}
}
