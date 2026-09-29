package processbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/process"
)

func bridgeTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var (
	bridgeNotice = events.NewTopic[string]("test.bridge.notice/1")
	bridgeChoose = events.NewHook[string, string]("test.bridge.choose/1")
	bridgeFlow   = events.NewHook[string, string]("test.bridge.flow/1")
	stringCodec  = JSONEventCodec[string]("utf8-json-string/1")
	bridgeEvents = []EventBinding{
		BindTopic(bridgeNotice, stringCodec, EventPublish|EventSubscribe),
		BindHook(bridgeChoose, stringCodec, stringCodec, EventPublish|EventSubscribe),
		BindHook(bridgeFlow, stringCodec, stringCodec, EventPublish|EventSubscribe),
	}
)

type eventControl interface {
	Publish(context.Context, string) error
	Select(context.Context, string) (string, bool, error)
	Flow(context.Context, string) (string, error)
}

var eventControlKey = gordis.NewKey[eventControl]("test.event-control/1")

type eventController struct{ binding events.Binding }

func (c eventController) Publish(ctx context.Context, value string) error {
	return c.binding.Publish(ctx, bridgeNotice, value)
}

func (c eventController) Select(ctx context.Context, value string) (string, bool, error) {
	return c.binding.Serial(ctx, bridgeChoose, value)
}

func (c eventController) Flow(ctx context.Context, value string) (string, error) {
	return c.binding.Waterfall(ctx, bridgeFlow, value, func(_ context.Context, input string) (string, error) {
		return input + "-base", nil
	})
}

var eventControlBinding = Bind(eventControlKey, func(c process.Caller) eventControl {
	return eventControlClient{caller: c}
}, func(control eventControl) process.CallHandler {
	return func(ctx context.Context, _ process.Caller, method string, raw json.RawMessage) (any, error) {
		var input string
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		switch method {
		case "publish":
			return nil, control.Publish(ctx, input)
		case "select":
			value, handled, err := control.Select(ctx, input)
			return struct {
				Value   string `json:"value"`
				Handled bool   `json:"handled"`
			}{value, handled}, err
		case "flow":
			return control.Flow(ctx, input)
		default:
			return nil, errors.New("unknown event control method")
		}
	}
})

type eventControlClient struct{ caller process.Caller }

func (c eventControlClient) Publish(ctx context.Context, value string) error {
	return c.caller.Call(ctx, "publish", value, nil)
}

func (c eventControlClient) Select(ctx context.Context, value string) (string, bool, error) {
	var result struct {
		Value   string `json:"value"`
		Handled bool   `json:"handled"`
	}
	err := c.caller.Call(ctx, "select", value, &result)
	return result.Value, result.Handled, err
}

func (c eventControlClient) Flow(ctx context.Context, value string) (string, error) {
	var result string
	err := c.caller.Call(ctx, "flow", value, &result)
	return result, err
}

type bridgeEventPlugin struct {
	Role     string `json:"role"`
	Priority int    `json:"priority,omitempty"`
}

func (*bridgeEventPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "bridge-events", Requires: []gordis.ServiceSpec{events.BusKey.Spec(), journalKey.Spec()},
		Provides: []gordis.ServiceSpec{eventControlKey.Spec()},
		New:      func() gordis.Plugin { return new(bridgeEventPlugin) },
	}
}

func (p *bridgeEventPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	if p.Role == "mount" {
		_, err := scope.Env().Mount(ctx, gordis.InstanceSpec{ID: "invisible", Plugin: "bridge-events"})
		return err
	}
	if p.Role == "bad-contract" {
		badTopic := events.NewTopic[int](bridgeNotice.ID())
		_, err := events.Bind(scope).On(badTopic, func(context.Context, int) error { return nil })
		return err
	}
	journal, err := gordis.Get(scope, journalKey)
	if err != nil {
		return err
	}
	binding := events.Bind(scope)
	if strings.HasPrefix(p.Role, "subscriber") {
		options := []events.OnOption{}
		if p.Priority != 0 {
			options = append(options, events.Priority(p.Priority))
		}
		if _, err := binding.On(bridgeNotice, func(ctx context.Context, value string) error {
			if err := journal.Record(ctx, p.Role+":"+value); err != nil {
				return err
			}
			if value == "error" {
				return errors.New("remote event failure")
			}
			if p.Role == "subscriber-crash" && value == "crash" {
				os.Exit(2)
			}
			if value == "block" {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}, options...); err != nil {
			return err
		}
		if _, err := binding.Handle(bridgeChoose, func(_ context.Context, value string) (string, bool, error) {
			return p.Role + ":" + value, true, nil
		}); err != nil {
			return err
		}
		if _, err := binding.Use(bridgeFlow, func(ctx context.Context, value string, next events.Next[string, string]) (string, error) {
			out, err := next(ctx, value+"-"+p.Role)
			if p.Role == "subscriber-double" {
				_, secondErr := next(ctx, value)
				return "", errors.Join(err, secondErr)
			}
			return "[" + out + "]", err
		}); err != nil {
			return err
		}
	}
	if p.Role == "after-ready" {
		if err := scope.AfterReady("publish-ready-event", func(ctx context.Context) error {
			return binding.Publish(ctx, bridgeNotice, "ready")
		}); err != nil {
			return err
		}
	}
	if p.Role == "subscriber-hang-cleanup" {
		if err := scope.Defer("hang-cleanup", func(context.Context) error {
			select {}
		}); err != nil {
			return err
		}
	}
	return gordis.Provide[eventControl](scope, eventControlKey, eventController{binding: binding})
}

func TestBridgeEventChild(t *testing.T) {
	if os.Getenv("GORDIS_EVENT_BRIDGE_CHILD") == "" {
		return
	}
	err := Serve(context.Background(), os.Stdin, os.Stdout, ServeOptions{
		Identity: process.Identity{
			Protocol: process.Protocol, Package: "bridge-events", Version: "1.0.0",
		},
		Limits:   process.Limits{MaxPending: 8, Queue: 8},
		Plugin:   new(bridgeEventPlugin),
		Requires: []Binding{journalBinding},
		Provides: []Binding{eventControlBinding},
		Events:   bridgeEvents,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func eventChildOptions(raw json.RawMessage) process.Options {
	return process.Options{
		Path: os.Args[0], Args: []string{"-test.run=^TestBridgeEventChild$"},
		Env: append(os.Environ(), "GORDIS_EVENT_BRIDGE_CHILD=1", "GORACE=atexit_sleep_ms=0"), Config: raw,
		Identity: process.Identity{
			Protocol: process.Protocol, Package: "bridge-events", Version: "1.0.0",
		},
		Limits:         process.Limits{MaxPending: 8, Queue: 8},
		StartupTimeout: 3 * time.Second, ExitGrace: 2 * time.Second,
	}
}

func eventAdapter(onClient func(*process.Client), bindings []EventBinding) gordis.Plugin {
	return eventAdapterWith(onClient, bindings, func(raw json.RawMessage) process.Options {
		return eventChildOptions(raw)
	})
}

func eventAdapterWith(
	onClient func(*process.Client),
	bindings []EventBinding,
	options func(json.RawMessage) process.Options,
) gordis.Plugin {
	adapter := Adapter{
		ID: "bridge-events", Requires: []Binding{journalBinding},
		Provides: []Binding{eventControlBinding}, Events: bindings,
		Resolve:  func(raw json.RawMessage) (process.Options, error) { return options(raw), nil },
		OnClient: onClient,
	}
	plugin, err := adapter.Plugin()
	if err != nil {
		panic(err)
	}
	return plugin
}

type synchronizedLog struct {
	mu     sync.Mutex
	values []string
}

func (l *synchronizedLog) Record(_ context.Context, value string) error {
	l.mu.Lock()
	l.values = append(l.values, value)
	l.mu.Unlock()
	return nil
}

func (l *synchronizedLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.values...)
}

type eventBridgeFixture struct {
	t       *testing.T
	host    *gordis.Host
	log     *synchronizedLog
	clients []*process.Client
	pub     *gordis.Scope
	control eventControl
}

func newEventBridgeFixture(t *testing.T, plugins ...gordis.Plugin) *eventBridgeFixture {
	t.Helper()
	fixture := &eventBridgeFixture{t: t, log: new(synchronizedLog)}
	provider := newTestPlugin("event-journal", nil, []gordis.ServiceSpec{journalKey.Spec()}, func(_ context.Context, scope *gordis.Scope) error {
		return gordis.Provide[journal](scope, journalKey, fixture.log)
	})
	publisher := newTestPlugin("local-event-publisher", []gordis.ServiceSpec{events.BusKey.Spec()}, nil, func(_ context.Context, scope *gordis.Scope) error {
		fixture.pub = scope
		return nil
	})
	all := []gordis.Plugin{new(events.Plugin), provider, publisher}
	all = append(all, plugins...)
	host, err := gordis.NewHost(all...)
	if err != nil {
		t.Fatal(err)
	}
	fixture.host = host
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = host.Shutdown(ctx)
	})
	fixture.mount(gordis.InstanceSpec{ID: "events", Plugin: events.PluginID, Config: json.RawMessage(`{"maxParallel":8}`)}, host.Env())
	fixture.mount(gordis.InstanceSpec{ID: "journal", Plugin: "event-journal"}, host.Env())
	fixture.mount(gordis.InstanceSpec{ID: "publisher", Plugin: "local-event-publisher"}, host.Env())
	return fixture
}

func (f *eventBridgeFixture) mount(spec gordis.InstanceSpec, env gordis.Env) *gordis.Instance {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	instance, err := env.MountReady(ctx, spec)
	if err != nil {
		f.t.Fatal(err)
	}
	return instance
}

func (f *eventBridgeFixture) mountRemote(id, role, controlLabel string) *gordis.Instance {
	raw, _ := json.Marshal(map[string]string{"role": role})
	env := f.host.Env().Isolate(eventControlKey, controlLabel)
	return f.mount(gordis.InstanceSpec{ID: id, Plugin: "bridge-events", Config: raw}, env)
}

func localSubscriber(id string, log *synchronizedLog) gordis.Plugin {
	return newTestPlugin(id, []gordis.ServiceSpec{events.BusKey.Spec()}, nil, func(_ context.Context, scope *gordis.Scope) error {
		_, err := events.Bind(scope).On(bridgeNotice, func(ctx context.Context, value string) error {
			return log.Record(ctx, "local:"+value)
		})
		return err
	})
}

func controlConsumer(id string, target *eventControl) gordis.Plugin {
	return newTestPlugin(id, []gordis.ServiceSpec{eventControlKey.Spec()}, nil, func(_ context.Context, scope *gordis.Scope) error {
		control, err := gordis.Get(scope, eventControlKey)
		if err == nil {
			*target = control
		}
		return err
	})
}

func TestProcessEventFourExecutionCombinations(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"local-to-local", func(t *testing.T) {
			log := new(synchronizedLog)
			fixture := newEventBridgeFixture(t, localSubscriber("local-subscriber", log))
			fixture.mount(gordis.InstanceSpec{ID: "subscriber", Plugin: "local-subscriber"}, fixture.host.Env())
			if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "hello"); err != nil {
				t.Fatal(err)
			}
			if got := log.snapshot(); !reflect.DeepEqual(got, []string{"local:hello"}) {
				t.Fatal(got)
			}
		}},
		{"local-to-process", func(t *testing.T) {
			fixture := newEventBridgeFixture(t, eventAdapter(nil, bridgeEvents))
			fixture.mountRemote("remote-subscriber", "subscriber", "remote")
			if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "hello"); err != nil {
				t.Fatal(err)
			}
			if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{"subscriber:hello"}) {
				t.Fatal(got)
			}
		}},
		{"process-to-local", func(t *testing.T) {
			var control eventControl
			log := new(synchronizedLog)
			fixture := newEventBridgeFixture(t,
				eventAdapter(nil, bridgeEvents), localSubscriber("local-subscriber", log),
				controlConsumer("control-consumer", &control),
			)
			fixture.mount(gordis.InstanceSpec{ID: "subscriber", Plugin: "local-subscriber"}, fixture.host.Env())
			fixture.mountRemote("remote-publisher", "publisher", "remote-control")
			fixture.mount(gordis.InstanceSpec{ID: "consumer", Plugin: "control-consumer"}, fixture.host.Env().Isolate(eventControlKey, "remote-control"))
			if err := control.Publish(bridgeTestContext(t), "hello"); err != nil {
				t.Fatal(err)
			}
			if got := log.snapshot(); !reflect.DeepEqual(got, []string{"local:hello"}) {
				t.Fatal(got)
			}
		}},
		{"process-to-process", func(t *testing.T) {
			var control eventControl
			fixture := newEventBridgeFixture(t,
				eventAdapter(nil, bridgeEvents), controlConsumer("control-consumer", &control),
			)
			fixture.mountRemote("remote-subscriber", "subscriber", "subscriber-control")
			fixture.mountRemote("remote-publisher", "publisher", "publisher-control")
			fixture.mount(gordis.InstanceSpec{ID: "consumer", Plugin: "control-consumer"}, fixture.host.Env().Isolate(eventControlKey, "publisher-control"))
			if err := control.Publish(bridgeTestContext(t), "hello"); err != nil {
				t.Fatal(err)
			}
			if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{"subscriber:hello"}) {
				t.Fatal(got)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestProcessEventSelectionAndNestedWaterfall(t *testing.T) {
	var control eventControl
	fixture := newEventBridgeFixture(t,
		eventAdapter(nil, bridgeEvents), controlConsumer("flow-consumer", &control),
	)
	fixture.mountRemote("remote-subscriber", "subscriber", "subscriber-control")
	fixture.mountRemote("remote-publisher", "publisher", "publisher-control")
	fixture.mount(gordis.InstanceSpec{ID: "consumer", Plugin: "flow-consumer"}, fixture.host.Env().Isolate(eventControlKey, "publisher-control"))

	selected, handled, err := control.Select(bridgeTestContext(t), "choice")
	if err != nil || !handled || selected != "subscriber:choice" {
		t.Fatalf("selection: %q %v %v", selected, handled, err)
	}
	flow, err := control.Flow(bridgeTestContext(t), "start")
	if err != nil || flow != "[start-subscriber-base]" {
		t.Fatalf("waterfall: %q %v", flow, err)
	}
}

func TestProcessAfterReadyWaitsForHostReady(t *testing.T) {
	log := new(synchronizedLog)
	fixture := newEventBridgeFixture(t,
		eventAdapter(nil, bridgeEvents), localSubscriber("ready-subscriber", log),
	)
	fixture.mount(gordis.InstanceSpec{ID: "subscriber", Plugin: "ready-subscriber"}, fixture.host.Env())
	fixture.mountRemote("remote-publisher", "after-ready", "publisher-control")
	waitFor(t, func() bool { return reflect.DeepEqual(log.snapshot(), []string{"local:ready"}) })
}

func TestProcessEventUsesHostTopicAndSourceView(t *testing.T) {
	sourceKey := gordis.NewKey[int]("test.event-source/1")
	var sourceScope *gordis.Scope
	sourcePlugin := newTestPlugin(
		"source-publisher",
		[]gordis.ServiceSpec{events.BusKey.Spec()},
		[]gordis.ServiceSpec{sourceKey.Spec()},
		func(_ context.Context, scope *gordis.Scope) error {
			sourceScope = scope
			return gordis.Provide(scope, sourceKey, 1)
		},
	)
	fixture := newEventBridgeFixture(t, eventAdapter(nil, bridgeEvents), sourcePlugin)
	mountSubscriber := func(id, role, controlLabel, sourceLabel string) {
		raw, _ := json.Marshal(map[string]string{"role": role})
		env := fixture.host.Env().
			Isolate(eventControlKey, controlLabel).
			Isolate(bridgeNotice, "tenant-a").
			Isolate(sourceKey, sourceLabel)
		fixture.mount(gordis.InstanceSpec{ID: id, Plugin: "bridge-events", Config: raw}, env)
	}
	mountSubscriber("remote-a", "subscriber-a", "control-a", "source-a")
	mountSubscriber("remote-b", "subscriber-b", "control-b", "source-b")
	fixture.mount(
		gordis.InstanceSpec{ID: "source", Plugin: "source-publisher"},
		fixture.host.Env().Isolate(bridgeNotice, "tenant-a").Isolate(sourceKey, "source-a"),
	)

	if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "wrong-topic"); err != nil {
		t.Fatal(err)
	}
	if got := fixture.log.snapshot(); len(got) != 0 {
		t.Fatalf("Topic isolation was bypassed: %v", got)
	}
	if err := events.Bind(sourceScope).PublishFrom(
		bridgeTestContext(t), sourceKey, bridgeNotice, "matched",
	); err != nil {
		t.Fatal(err)
	}
	if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{"subscriber-a:matched"}) {
		t.Fatalf("source routing: %v", got)
	}
}

func TestProcessEventOrderingAndHandlerError(t *testing.T) {
	fixture := newEventBridgeFixture(t, eventAdapter(nil, bridgeEvents))
	mount := func(id, role, controlLabel string, priority int) {
		raw, _ := json.Marshal(map[string]any{"role": role, "priority": priority})
		fixture.mount(
			gordis.InstanceSpec{ID: id, Plugin: "bridge-events", Config: raw},
			fixture.host.Env().Isolate(eventControlKey, controlLabel),
		)
	}
	mount("remote-low", "subscriber-low", "control-low", 0)
	mount("remote-high", "subscriber-high", "control-high", 10)
	if err := events.Bind(fixture.pub).Emit(bridgeTestContext(t), bridgeNotice, "ordered"); err != nil {
		t.Fatal(err)
	}
	if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{
		"subscriber-high:ordered", "subscriber-low:ordered",
	}) {
		t.Fatalf("remote priority order: %v", got)
	}
	if err := events.Bind(fixture.pub).Emit(bridgeTestContext(t), bridgeNotice, "error"); err == nil || !strings.Contains(err.Error(), "remote event failure") {
		t.Fatalf("remote handler error: %v", err)
	}
}

func TestProcessEventContinuationIsSingleUse(t *testing.T) {
	fixture := newEventBridgeFixture(t, eventAdapter(nil, bridgeEvents))
	fixture.mountRemote("remote-subscriber", "subscriber-double", "subscriber-control")
	if _, err := events.Bind(fixture.pub).Waterfall(
		bridgeTestContext(t), bridgeFlow, "start",
		func(_ context.Context, input string) (string, error) { return input + "-base", nil },
	); !errors.Is(err, events.ErrNextCalled) {
		t.Fatalf("double next: %v", err)
	}
}

func TestProcessEventContinuationExpiresAfterDelivery(t *testing.T) {
	set := continuationSet{}
	binding := bridgeEvents[2]
	token := set.add(binding, func(_ context.Context, input any) (any, error) { return input, nil })
	set.remove(token)
	response := set.call(context.Background(), eventCallbackRequest{
		Token: token, Contract: binding.id, Wire: binding.wire, Payload: json.RawMessage(`"late"`),
	})
	if err := decodeEventError(response.Error); !errors.Is(err, events.ErrNextExpired) {
		t.Fatalf("expired continuation: %v", err)
	}
}

func TestProcessEventCancellationBackpressureAndCleanup(t *testing.T) {
	var client *process.Client
	fixture := newEventBridgeFixture(t, eventAdapter(func(c *process.Client) { client = c }, bridgeEvents))
	instance := fixture.mountRemote("remote-subscriber", "subscriber", "remote")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := events.Bind(fixture.pub).Publish(ctx, bridgeNotice, "block")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{"subscriber:block"}) {
		t.Fatal(got)
	}

	if err := instance.Unmount(bridgeTestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "after-unmount"); err != nil {
		t.Fatal(err)
	}
	if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{"subscriber:block"}) {
		t.Fatalf("subscription survived cleanup: %v", got)
	}
	if client == nil || !client.Snapshot().Reaped || !client.Snapshot().HandlersComplete {
		t.Fatalf("process not fully cleaned: %+v", client)
	}
}

func TestProcessEventBackpressureIsReportedWithoutRetry(t *testing.T) {
	adapter := eventAdapterWith(nil, bridgeEvents, func(raw json.RawMessage) process.Options {
		options := eventChildOptions(raw)
		options.Limits.MaxPending = 1
		return options
	})
	fixture := newEventBridgeFixture(t, adapter)
	fixture.mountRemote("remote-subscriber", "subscriber", "remote")
	waitFor(t, func() bool {
		for _, snapshot := range fixture.host.Snapshot() {
			if snapshot.QualifiedID != "remote-subscriber" {
				continue
			}
			for _, task := range snapshot.Tasks {
				if task.Name == "processbridge event ready" {
					return !task.Running && task.Error == ""
				}
			}
		}
		return false
	})

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- events.Bind(fixture.pub).Publish(firstCtx, bridgeNotice, "block")
	}()
	deadline := time.Now().Add(15 * time.Second)
	for !reflect.DeepEqual(fixture.log.snapshot(), []string{"subscriber:block"}) {
		select {
		case err := <-firstDone:
			t.Fatalf("first delivery failed before entering handler: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("remote blocking handler did not start")
		}
		time.Sleep(time.Millisecond)
	}
	err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "second")
	if !errors.Is(err, process.ErrBackpressure) {
		t.Fatalf("backpressure: %v", err)
	}
	cancelFirst()
	if err := receive(t, firstDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("first delivery cancellation: %v", err)
	}
	if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{"subscriber:block"}) {
		t.Fatalf("backpressured delivery was retried or invoked: %v", got)
	}
}

func TestProcessEventRestartReplacesGenerationSubscription(t *testing.T) {
	var mu sync.Mutex
	var clients []*process.Client
	fixture := newEventBridgeFixture(t, eventAdapter(func(client *process.Client) {
		mu.Lock()
		clients = append(clients, client)
		mu.Unlock()
	}, bridgeEvents))
	instance := fixture.mountRemote("remote-subscriber", "subscriber", "remote")
	if err := instance.Restart(bridgeTestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := instance.WaitReady(bridgeTestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "after-restart"); err != nil {
		t.Fatal(err)
	}
	if got := fixture.log.snapshot(); !reflect.DeepEqual(got, []string{"subscriber:after-restart"}) {
		t.Fatalf("old generation subscription survived: %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(clients) != 2 || !clients[0].Snapshot().Reaped || clients[0].Snapshot().Generation == clients[1].Snapshot().Generation {
		t.Fatalf("generation clients: %+v", clients)
	}
}

func TestProcessEventAbruptExitRemovesSubscriptions(t *testing.T) {
	for _, mode := range []string{"crash", "kill"} {
		t.Run(mode, func(t *testing.T) {
			var client *process.Client
			fixture := newEventBridgeFixture(t, eventAdapter(func(value *process.Client) { client = value }, bridgeEvents))
			role := "subscriber"
			if mode == "crash" {
				role = "subscriber-crash"
			}
			fixture.mountRemote("remote-subscriber", role, "remote")
			if mode == "crash" {
				if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "crash"); err == nil {
					t.Fatal("crashing delivery unexpectedly succeeded")
				}
			} else {
				processHandle, err := os.FindProcess(client.Snapshot().PID)
				if err != nil {
					t.Fatal(err)
				}
				if err := processHandle.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, func() bool {
				for _, snapshot := range fixture.host.Snapshot() {
					if snapshot.QualifiedID == "remote-subscriber" {
						return snapshot.Phase == gordis.PhaseStopped && snapshot.Failure != ""
					}
				}
				return false
			})
			before := fixture.log.snapshot()
			if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "after-exit"); err != nil {
				t.Fatal(err)
			}
			if got := fixture.log.snapshot(); !reflect.DeepEqual(got, before) {
				t.Fatalf("subscription survived %s: before=%v after=%v", mode, before, got)
			}
			if !client.Snapshot().Reaped || !client.Snapshot().HandlersComplete {
				t.Fatalf("abrupt process cleanup incomplete: %+v", client.Snapshot())
			}
		})
	}
}

func TestProcessEventHungRemoteCleanupStillRemovesSubscription(t *testing.T) {
	adapter := eventAdapterWith(nil, bridgeEvents, func(raw json.RawMessage) process.Options {
		options := eventChildOptions(raw)
		options.ExitGrace = 100 * time.Millisecond
		return options
	})
	fixture := newEventBridgeFixture(t, adapter)
	instance := fixture.mountRemote("remote-subscriber", "subscriber-hang-cleanup", "remote")
	if err := instance.Unmount(bridgeTestContext(t)); err == nil {
		t.Fatal("hung remote cleanup was reported complete")
	}
	if err := events.Bind(fixture.pub).Publish(bridgeTestContext(t), bridgeNotice, "after-hung-cleanup"); err != nil {
		t.Fatal(err)
	}
	if got := fixture.log.snapshot(); len(got) != 0 {
		t.Fatalf("subscription survived hung cleanup: %v", got)
	}
}

func TestProcessEventWirePreflightAndEnvCapabilityRejection(t *testing.T) {
	invalid := BindTopic(bridgeNotice, EventCodec[string]{ID: "missing"}, EventPublish)
	if _, err := (Adapter{ID: "invalid", Events: []EventBinding{invalid}, Resolve: func(json.RawMessage) (process.Options, error) {
		return process.Options{}, nil
	}}).Plugin(); err == nil {
		t.Fatal("accepted missing event codec")
	}

	mismatchedEvents := append([]EventBinding(nil), bridgeEvents...)
	mismatchedEvents[0] = BindTopic(
		bridgeNotice, JSONEventCodec[string]("utf8-json-string/2"), EventPublish|EventSubscribe,
	)
	mismatchFixture := newEventBridgeFixture(t, eventAdapter(nil, mismatchedEvents))
	rawSubscriber, _ := json.Marshal(map[string]string{"role": "subscriber"})
	mismatch, err := mismatchFixture.host.Env().Isolate(eventControlKey, "mismatch-control").Mount(
		bridgeTestContext(t),
		gordis.InstanceSpec{ID: "wire-mismatch", Plugin: "bridge-events", Config: rawSubscriber},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := mismatch.WaitReady(bridgeTestContext(t)); err == nil || !strings.Contains(err.Error(), "missing contract gordis.event/1:") {
		t.Fatalf("wire mismatch reached activation: %v", err)
	}
	if got := mismatchFixture.log.snapshot(); len(got) != 0 {
		t.Fatalf("remote Plugin ran before wire validation: %v", got)
	}

	fixture := newEventBridgeFixture(t, eventAdapter(nil, bridgeEvents))
	badRaw, _ := json.Marshal(map[string]string{"role": "bad-contract"})
	badContract, err := fixture.host.Env().Isolate(eventControlKey, "bad-control").Mount(
		bridgeTestContext(t),
		gordis.InstanceSpec{ID: "bad-contract", Plugin: "bridge-events", Config: badRaw},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := badContract.WaitReady(bridgeTestContext(t)); err == nil || !strings.Contains(err.Error(), "local event contract differs") {
		t.Fatalf("mismatched local event contract reached Ready: %v", err)
	}

	raw, _ := json.Marshal(map[string]string{"role": "mount"})
	instance, err := fixture.host.Env().Isolate(eventControlKey, "mount-control").Mount(bridgeTestContext(t), gordis.InstanceSpec{
		ID: "remote-mount", Plugin: "bridge-events", Config: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = instance.WaitReady(bridgeTestContext(t))
	if !errors.Is(err, gordis.ErrEnvUnavailable) && (err == nil || !strings.Contains(err.Error(), gordis.ErrEnvUnavailable.Error())) {
		t.Fatalf("remote Env control was not explicitly rejected: %v", err)
	}
}
