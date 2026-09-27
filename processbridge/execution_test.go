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
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/process/processtest"
)

type journal interface {
	Record(context.Context, string) error
}
type sampleService interface {
	Sample(context.Context, string) (string, error)
}

var journalKey = gordis.NewKey[journal]("test.journal/1")
var sampleKey = gordis.NewKey[sampleService]("test.sample/1")

type sampleFunc func(context.Context, string) (string, error)

func (f sampleFunc) Sample(ctx context.Context, s string) (string, error) { return f(ctx, s) }

type recordFunc func(context.Context, string) error

func (f recordFunc) Record(ctx context.Context, s string) error { return f(ctx, s) }

var journalBinding = Bind(journalKey, func(c process.Caller) journal {
	return recordFunc(func(ctx context.Context, s string) error { return c.Call(ctx, "record", s, nil) })
}, func(j journal) process.CallHandler {
	return func(ctx context.Context, _ process.Caller, method string, raw json.RawMessage) (any, error) {
		if method != "record" {
			return nil, errors.New("bad journal method")
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return nil, j.Record(ctx, s)
	}
})
var sampleBinding = Bind(sampleKey, func(c process.Caller) sampleService {
	return sampleFunc(func(ctx context.Context, s string) (string, error) {
		var out string
		err := c.Call(ctx, "sample", s, &out)
		return out, err
	})
}, func(s sampleService) process.CallHandler {
	return func(ctx context.Context, _ process.Caller, method string, raw json.RawMessage) (any, error) {
		if method != "sample" {
			return nil, errors.New("bad sample method")
		}
		var arg string
		if err := json.Unmarshal(raw, &arg); err != nil {
			return nil, err
		}
		return s.Sample(ctx, arg)
	}
})

// Every execution mode below calls this exact Plugin/Activate, including its task,
// initialization dependency call, OnStop and reverse-order Defer callbacks.
type fixturePlugin struct {
	Mode string `json:"mode"`
}

func (*fixturePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "fixture",
		Requires: []gordis.ServiceSpec{journalKey.Spec()},
		Provides: []gordis.ServiceSpec{sampleKey.Spec()},
		New:      func() gordis.Plugin { return new(fixturePlugin) },
	}
}

func (p *fixturePlugin) Activate(ctx context.Context, s *gordis.Scope) error {
	j, err := gordis.Get(s, journalKey)
	if err != nil {
		return err
	}
	if err := j.Record(ctx, "start"); err != nil {
		return err
	}
	if err := s.Defer("first", func(ctx context.Context) error {
		if p.Mode == "hang" {
			select {}
		}
		if err := j.Record(ctx, "defer:first"); err != nil {
			return err
		}
		if p.Mode == "cleanup-error" {
			return errors.New("cleanup fixture error")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.Defer("second", func(ctx context.Context) error { return j.Record(ctx, "defer:second") }); err != nil {
		return err
	}
	if err := s.OnStop("entry", func(ctx context.Context) error { return j.Record(ctx, "stop") }); err != nil {
		return err
	}
	failed := make(chan struct{}, 1)
	if err := s.Go("worker", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return j.Record(context.Background(), "task:canceled")
		case <-failed:
			return errors.New("worker fixture error")
		}
	}); err != nil {
		return err
	}
	if p.Mode == "init-error" {
		return errors.New("initialization fixture error")
	}
	if p.Mode == "missing" {
		return nil
	}
	return gordis.Provide(s, sampleKey, sampleService(sampleFunc(func(ctx context.Context, arg string) (string, error) {
		if err := j.Record(ctx, "sample:"+arg); err != nil {
			return "", err
		}
		if arg == "fail" {
			failed <- struct{}{}
		}
		return fmt.Sprintf("%s/%d:%s", s.ID(), s.Generation(), arg), nil
	})))
}

type testPlugin struct {
	spec  gordis.PluginSpec
	start func(context.Context, *gordis.Scope) error
}

func newTestPlugin(
	id string,
	requires, provides []gordis.ServiceSpec,
	start func(context.Context, *gordis.Scope) error,
) gordis.Plugin {
	var spec gordis.PluginSpec
	spec = gordis.PluginSpec{ID: id, Requires: requires, Provides: provides}
	spec.New = func() gordis.Plugin { return &testPlugin{spec: spec, start: start} }
	return &testPlugin{spec: spec, start: start}
}

func (p *testPlugin) Spec() gordis.PluginSpec { return p.spec }

func (p *testPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	return p.start(ctx, scope)
}

func TestBridgeChild(t *testing.T) {
	if os.Getenv("GORDIS_BRIDGE_CHILD") == "" {
		return
	}
	err := Serve(context.Background(), os.Stdin, os.Stdout, ServeOptions{
		Identity: process.Identity{
			Protocol: process.Protocol,
			Package:  "fixture",
			Version:  "3.0.0",
		},
		Plugin:   new(fixturePlugin),
		Requires: []Binding{journalBinding},
		Provides: []Binding{sampleBinding},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func childOptions(raw json.RawMessage) process.Options {
	return process.Options{
		Path:   os.Args[0],
		Args:   []string{"-test.run=^TestBridgeChild$"},
		Env:    append(os.Environ(), "GORDIS_BRIDGE_CHILD=1"),
		Config: raw,
		Identity: process.Identity{
			Protocol: process.Protocol,
			Package:  "fixture",
			Version:  "3.0.0",
		},
		ExitGrace:      2 * time.Second,
		StartupTimeout: 3 * time.Second,
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("channel timed out")
		var v T
		return v
	}
}

type logbook struct {
	mu     sync.Mutex
	events []string
}

func (l *logbook) Record(_ context.Context, s string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
	return nil
}

func (l *logbook) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func TestPluginLifecycleConsistency(t *testing.T) {
	for _, mode := range []string{"normal", "init-error", "missing", "task-error", "cleanup-error"} {
		t.Run(mode, func(t *testing.T) {
			var reference []string
			for _, driver := range []string{"host", "runner", "process"} {
				t.Run(driver, func(t *testing.T) {
					log := &logbook{}
					raw, _ := json.Marshal(map[string]string{"mode": mode})
					var service sampleService
					var startErr, stopErr error
					var residual bool
					if driver == "runner" {
						failed := make(chan error, 1)
						fixture := new(fixturePlugin)
						spec := fixture.Spec()
						r := newPluginRun(spec, "sample", 1, map[string]any{journalKey.Name(): log}, func(err error) { failed <- err })
						startErr = r.start(context.Background(), spec, raw)
						if startErr == nil {
							service = r.values[sampleKey.Name()].(sampleService)
							if value, err := service.Sample(context.Background(), "ok"); err != nil || value != "sample/1:ok" {
								t.Fatal(value, err)
							}
							if mode == "task-error" {
								_, _ = service.Sample(context.Background(), "fail")
								receive(t, failed)
							}
						}
						r.quiesce()
						if _, err := r.controller.Scope().Acquire(); !errors.Is(err, gordis.ErrClosed) {
							t.Fatal(err)
						}
						stopErr = r.dispose()
						residual = r.controller.Retains()
						if !r.controller.Snapshot().CleanupComplete || r.controller.Snapshot().Leases != 0 {
							t.Fatal(r.controller.Snapshot())
						}
					} else {
						fixture := gordis.Plugin(new(fixturePlugin))
						var client *process.Client
						if driver == "process" {
							adapter := Adapter{
								ID:       "fixture",
								Requires: []Binding{journalBinding},
								Provides: []Binding{sampleBinding},
								Resolve: func(raw json.RawMessage) (process.Options, error) {
									return childOptions(raw), nil
								},
								OnClient: func(c *process.Client) { client = c },
							}
							var err error
							fixture, err = adapter.Plugin()
							if err != nil {
								t.Fatal(err)
							}
						}
						providerStart := func(_ context.Context, s *gordis.Scope) error {
							return gordis.Provide(s, journalKey, journal(log))
						}
						provider := newTestPlugin(
							"journal", nil, []gordis.ServiceSpec{journalKey.Spec()}, providerStart,
						)
						consumerStart := func(_ context.Context, s *gordis.Scope) error {
							var err error
							service, err = gordis.Get(s, sampleKey)
							return err
						}
						consumer := newTestPlugin(
							"consumer", []gordis.ServiceSpec{sampleKey.Spec()}, nil, consumerStart,
						)
						h, err := gordis.NewHost([]gordis.Plugin{provider, fixture, consumer})
						if err != nil {
							t.Fatal(err)
						}
						changes := h.Changes()
						changes.Mount(h.Env(), gordis.InstanceSpec{ID: "journal", Plugin: "journal"})
						changes.Mount(h.Env(), gordis.InstanceSpec{ID: "sample", Plugin: "fixture", Config: raw})
						changes.Mount(h.Env(), gordis.InstanceSpec{ID: "consumer", Plugin: "consumer"})
						operation, err := changes.Apply(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						startErr = operation.WaitReady(context.Background())
						if startErr == nil {
							if value, err := service.Sample(context.Background(), "ok"); err != nil || value != "sample/1:ok" {
								t.Fatal(value, err)
							}
							if mode == "task-error" {
								_, _ = service.Sample(context.Background(), "fail")
								waitFor(t, func() bool {
									for _, s := range h.Snapshot() {
										if s.QualifiedID == "sample" {
											return s.CleanupComplete
										}
									}
									return false
								})
							}
						}
						stopErr = stopHost(t, h)
						for _, s := range h.Snapshot() {
							if s.QualifiedID == "sample" {
								residual = len(s.Residuals) > 0
								if mode == "task-error" && !strings.Contains(s.Failure, "worker fixture error") {
									t.Fatal(s)
								}
							}
						}
						if client != nil {
							processtest.AssertClosed(t, client)
							if !client.Snapshot().CleanupKnown || !client.Snapshot().Cleanup.Complete {
								t.Fatal(client.Snapshot())
							}
						}
					}
					if (mode == "init-error" || mode == "missing") != (startErr != nil) {
						t.Fatal("start", startErr)
					}
					if mode == "cleanup-error" && (stopErr == nil || !residual) {
						t.Fatal("cleanup failure lost", stopErr, residual)
					}
					if mode != "cleanup-error" && residual {
						t.Fatal("unexpected residual", stopErr)
					}
					events := log.list()
					if driver == "host" {
						reference = events
					} else if !reflect.DeepEqual(reference, events) {
						t.Fatalf("events %v; want %v", events, reference)
					}
				})
			}
		})
	}
}

func TestRunnerUsesAssignedIdentityAndRejectsCommitAfterFreeze(t *testing.T) {
	spec := new(fixturePlugin).Spec()
	r := newPluginRun(spec, "assigned", 57, map[string]any{journalKey.Name(): &logbook{}}, nil)
	if err := r.start(context.Background(), spec, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	value, err := r.values[sampleKey.Name()].(sampleService).Sample(context.Background(), "hello")
	if err != nil || value != "assigned/57:hello" {
		t.Fatal(value, err)
	}
	if err := r.dispose(); err != nil {
		t.Fatal(err)
	}
	r = newPluginRun(spec, "assigned", 58, map[string]any{journalKey.Name(): &logbook{}}, nil)
	r.controller.Freeze()
	if err := r.start(context.Background(), spec, json.RawMessage(`{}`)); err == nil || len(r.values) != 0 {
		t.Fatal("published frozen run", err)
	}
	_ = r.dispose()
}

type blockingJSON struct{ entered, release chan struct{} }

func (b blockingJSON) MarshalJSON() ([]byte, error) {
	close(b.entered)
	<-b.release
	return []byte(`"ok"`), nil
}

type callerFunc func(context.Context, string, any, any) error

func (f callerFunc) Call(c context.Context, m string, p, r any) error { return f(c, m, p, r) }

func TestAdmissionBarrierIncludesNotYetSerializedCalls(t *testing.T) {
	block := blockingJSON{make(chan struct{}), make(chan struct{})}
	sent := make(chan struct{})
	caller := callerFunc(func(_ context.Context, _ string, p, _ any) error {
		_, err := json.Marshal(p)
		close(sent)
		return err
	})
	gate := &callGate{peer: caller}
	result := make(chan error, 1)
	go func() { result <- (boundCaller{gate, "bound"}).Call(context.Background(), "test", block, nil) }()
	receive(t, block.entered)
	drained := make(chan struct{})
	go func() { gate.drain(); close(drained) }()
	waitFor(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return gate.closed })
	select {
	case <-drained:
		t.Fatal("quiesce overtook admitted call")
	default:
	}
	close(block.release)
	receive(t, sent)
	if err := receive(t, result); err != nil {
		t.Fatal(err)
	}
	receive(t, drained)
	if err := gate.Call(context.Background(), "test", nil, nil); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
}

func processHost(
	t *testing.T,
	mode string,
	j journal,
	consumerCleanup bool,
) (
	*gordis.Host,
	*process.Client,
	*gordis.Scope,
	sampleService,
	*gordis.Scope,
	*bool,
) {
	t.Helper()
	var client *process.Client
	var consumerScope, providerScope *gordis.Scope
	var service sampleService
	disposed := new(bool)
	adapter := Adapter{
		ID:       "fixture",
		Requires: []Binding{journalBinding},
		Provides: []Binding{sampleBinding},
		Resolve: func(raw json.RawMessage) (process.Options, error) {
			o := childOptions(raw)
			o.ExitGrace = 250 * time.Millisecond
			o.Env = append(o.Env, "GORACE=atexit_sleep_ms=0")
			return o, nil
		},
		OnClient: func(c *process.Client) { client = c },
	}
	plugin, err := adapter.Plugin()
	if err != nil {
		t.Fatal(err)
	}
	providerStart := func(_ context.Context, s *gordis.Scope) error {
		providerScope = s
		cleanup := func(context.Context) error {
			*disposed = true
			return nil
		}
		if err := s.Defer("provider", cleanup); err != nil {
			return err
		}
		return gordis.Provide(s, journalKey, j)
	}
	provider := newTestPlugin(
		"journal", nil, []gordis.ServiceSpec{journalKey.Spec()}, providerStart,
	)
	consumerStart := func(_ context.Context, s *gordis.Scope) error {
		consumerScope = s
		var err error
		service, err = gordis.Get(s, sampleKey)
		if err != nil {
			return err
		}
		if consumerCleanup {
			cleanup := func(ctx context.Context) error {
				_, err := service.Sample(ctx, "consumer-cleanup")
				return err
			}
			return s.Defer("use-fixed-dependency", cleanup)
		}
		return nil
	}
	consumer := newTestPlugin(
		"consumer", []gordis.ServiceSpec{sampleKey.Spec()}, nil, consumerStart,
	)
	raw, _ := json.Marshal(map[string]string{"mode": mode})
	h, err := gordis.NewHost([]gordis.Plugin{provider, plugin, consumer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Env().MountReady(context.Background(), gordis.InstanceSpec{
		ID: "journal", Plugin: "journal",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Env().MountReady(context.Background(), gordis.InstanceSpec{
		ID: "sample", Plugin: "fixture", Config: raw,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Env().MountReady(context.Background(), gordis.InstanceSpec{
		ID: "consumer", Plugin: "consumer",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})
	return h, client, consumerScope, service, providerScope, disposed
}

func TestFrozenProviderAndConsumerCleanupKeepFixedReferences(t *testing.T) {
	var provider *gordis.Scope
	var disposed *bool
	log := &logbook{}
	j := recordFunc(func(ctx context.Context, event string) error {
		if strings.HasPrefix(event, "defer:") || event == "sample:consumer-cleanup" {
			if release, err := provider.Acquire(); err == nil {
				release()
				return errors.New("provider not frozen")
			}
			if *disposed {
				return errors.New("provider released before consumer")
			}
		}
		return log.Record(ctx, event)
	})
	h, c, _, _, p, d := processHost(t, "normal", j, true)
	provider, disposed = p, d
	if err := stopHost(t, h); err != nil {
		t.Fatal(err)
	}
	if !*disposed {
		t.Fatal("provider was not disposed")
	}
	want := []string{"start", "sample:consumer-cleanup", "stop", "task:canceled", "defer:second", "defer:first"}
	if !reflect.DeepEqual(log.list(), want) {
		t.Fatal(log.list())
	}
	processtest.AssertClosed(t, c)
}

func TestRemoteTaskFailureFreezesClosureWhileProcessLives(t *testing.T) {
	stopEntered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	j := recordFunc(func(_ context.Context, event string) error {
		if event == "stop" {
			close(stopEntered)
			<-release
		}
		return nil
	})
	h, c, consumer, service, _, _ := processHost(t, "normal", j, false)
	if _, err := service.Sample(context.Background(), "fail"); err != nil {
		t.Fatal(err)
	}
	receive(t, stopEntered)
	if c.Snapshot().Reaped {
		t.Fatal("failure learned only from exit")
	}
	if release, err := consumer.Acquire(); err == nil {
		release()
		t.Fatal("consumer was not frozen")
	}
	once.Do(func() { close(release) })
	if err := stopHost(t, h); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Snapshot().Error, "worker fixture error") {
		t.Fatal(c.Snapshot())
	}
}

func TestKilledOrCrashedPluginRetainsUnknownCleanup(t *testing.T) {
	for _, mode := range []string{"hang", "crash"} {
		t.Run(mode, func(t *testing.T) {
			h, c, _, _, _, disposed := processHost(t, mode, &logbook{}, false)
			if mode == "crash" {
				p, err := os.FindProcess(c.Snapshot().PID)
				if err != nil {
					t.Fatal(err)
				}
				if err := p.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = c.Wait()
			}
			if err := stopHost(t, h); err == nil {
				t.Fatal("unknown cleanup forgotten")
			}
			d := c.Snapshot()
			processtest.AssertClosed(t, c)
			if d.CleanupKnown || *disposed {
				t.Fatal("unknown cleanup released dependency", d, *disposed)
			}
		})
	}
}

func TestStuckHostCleanupCallbackBlocksProviderAfterKill(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	j := recordFunc(func(_ context.Context, event string) error {
		if event == "defer:first" {
			close(entered)
			<-release
		}
		return nil
	})
	h, c, _, _, _, disposed := processHost(t, "normal", j, false)
	// Release before the host test cleanup even if an assertion fails.
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	stopped := make(chan error, 1)
	go func() { stopped <- h.Shutdown(context.Background()) }()
	receive(t, entered)
	waitFor(t, func() bool { return c.Snapshot().Reaped && c.Snapshot().IOComplete })
	d := c.Snapshot()
	if d.HandlersComplete || d.Handling != 1 || *disposed {
		t.Fatal(d, *disposed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := receive(t, stopped); err == nil {
		t.Fatal("lost cleanup residual")
	}
	processtest.AssertClosed(t, c)
	if *disposed {
		t.Fatal("provider disposed despite unknown cleanup")
	}
}

func stopHost(t *testing.T, h *gordis.Host) error {
	t.Helper()
	var err error
	waitFor(t, func() bool { err = h.Shutdown(context.Background()); return !errors.Is(err, gordis.ErrBusy) })
	return err
}

func TestBindingValidationAndRoutingKeepCapabilitiesSeparate(t *testing.T) {
	second := gordis.NewKey[journal]("test.journal.other/1")
	b := Bind(second, func(c process.Caller) journal {
		return recordFunc(func(ctx context.Context, s string) error { return c.Call(ctx, "record", s, nil) })
	}, func(j journal) process.CallHandler { return journalBinding.handler(j) })
	if err := match([]gordis.ServiceSpec{journalKey.Spec()}, []Binding{b}); err == nil {
		t.Fatal("accepted wrong binding")
	}
	if _, err := specs([]Binding{journalBinding, journalBinding}); err == nil {
		t.Fatal("accepted duplicate binding")
	}
	var firstEvents, secondEvents logbook
	h := router(map[string]process.CallHandler{
		journalKey.Name(): journalBinding.handler(&firstEvents),
		second.Name():     b.handler(&secondEvents),
	})
	caller := callerFunc(func(ctx context.Context, method string, p, _ any) error {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = h(ctx, nil, method, raw)
		return err
	})
	for _, name := range []string{journalKey.Name(), second.Name()} {
		if err := (boundCaller{caller, name}).Call(context.Background(), "record", name, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(firstEvents.list(), []string{journalKey.Name()}) ||
		!reflect.DeepEqual(secondEvents.list(), []string{second.Name()}) {
		t.Fatal(firstEvents.list(), secondEvents.list())
	}
	var rpc *process.RPCError
	err := (boundCaller{caller, "not-declared"}).Call(
		context.Background(), "record", "bad", nil,
	)
	if !errors.As(err, &rpc) || rpc.Code != "binding" {
		t.Fatal(err)
	}
}

func TestObserverPanicCannotOrphanStartedProcess(t *testing.T) {
	var client *process.Client
	adapter := Adapter{
		ID:       "fixture",
		Requires: []Binding{journalBinding},
		Provides: []Binding{sampleBinding},
		Resolve: func(raw json.RawMessage) (process.Options, error) {
			return childOptions(raw), nil
		},
		OnClient: func(c *process.Client) {
			client = c
			panic("observer failure")
		},
	}
	plugin, err := adapter.Plugin()
	if err != nil {
		t.Fatal(err)
	}
	providerStart := func(_ context.Context, s *gordis.Scope) error {
		return gordis.Provide(s, journalKey, journal(&logbook{}))
	}
	provider := newTestPlugin(
		"journal", nil, []gordis.ServiceSpec{journalKey.Spec()}, providerStart,
	)
	h, err := gordis.NewHost([]gordis.Plugin{provider, plugin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "journal", Plugin: "journal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "sample", Plugin: "fixture"}); err == nil || !strings.Contains(err.Error(), "observer failure") {
		t.Fatal(err)
	}
	if err := stopHost(t, h); err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("observer never called")
	}
	processtest.AssertClosed(t, client)
}

func TestFreezeDuringRemoteStartupPreservesCleanupResidual(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var client *process.Client
	adapter := Adapter{
		ID:       "fixture",
		Requires: []Binding{journalBinding},
		Provides: []Binding{sampleBinding},
		Resolve: func(raw json.RawMessage) (process.Options, error) {
			return childOptions(raw), nil
		},
		OnClient: func(c *process.Client) { client = c },
	}
	plugin, err := adapter.Plugin()
	if err != nil {
		t.Fatal(err)
	}
	j := recordFunc(func(_ context.Context, event string) error {
		if event == "start" {
			close(entered)
			<-release
		}
		return nil
	})
	spec := plugin.Spec()
	r := newPluginRun(spec, "sample", 41, map[string]any{journalKey.Name(): j}, nil)
	started := make(chan error, 1)
	go func() { started <- r.start(context.Background(), spec, json.RawMessage(`{"mode":"cleanup-error"}`)) }()
	receive(t, entered)
	r.controller.Freeze()
	close(release)
	if err := receive(t, started); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal("frozen start published", err)
	}
	if err := r.dispose(); err == nil || !r.controller.Retains() {
		t.Fatal("startup cleanup error lost", err, r.controller.Snapshot())
	}
	if client == nil {
		t.Fatal("no child")
	}
	processtest.AssertClosed(t, client)
	if client.Snapshot().Generation != 41 || !client.Snapshot().CleanupKnown {
		t.Fatal(client.Snapshot())
	}
}
