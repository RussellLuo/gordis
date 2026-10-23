package rpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture/contract"
)

type fxEvent struct{ version, input string }
type fxControlledCallback struct {
	Version string `json:"version"`
	events  chan fxEvent
	hook    func(context.Context, string) error
}

func (p *fxControlledCallback) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "controlled-callback", Provides: []gordis.ServiceSpec{contract.CallbackKey.Spec()},
		New: func() gordis.Plugin { return &fxControlledCallback{Version: "old", events: p.events, hook: p.hook} }}
}
func (p *fxControlledCallback) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("provider cleanup", func(context.Context) error {
		p.events <- fxEvent{p.Version, "provider-cleanup"}
		return nil
	}); err != nil {
		return err
	}
	return scope.Provide(contract.CallbackKey, contract.Text(p))
}
func (p *fxControlledCallback) Call(ctx context.Context, input string) (string, error) {
	p.events <- fxEvent{p.Version, input}
	if p.hook != nil {
		if err := p.hook(ctx, input); err != nil {
			return "", err
		}
	}
	return p.Version + "/" + input, nil
}

func fxReceive[T any](t *testing.T, ctx context.Context, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}
func fxEventUntil(t *testing.T, ctx context.Context, events <-chan fxEvent, input string) []fxEvent {
	t.Helper()
	var seen []fxEvent
	for {
		event := fxReceive(t, ctx, events)
		seen = append(seen, event)
		if event.input == input {
			return seen
		}
	}
}
func fxBuild(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "./testdata/process").CombinedOutput(); err != nil {
		t.Fatalf("build child: %v\n%s", err, output)
	}
	return binary
}
func fxPrototype(t *testing.T, binary string) *rpc.ProcessPlugin {
	t.Helper()
	p, err := rpc.NewProcess(rpc.ProcessOptions{PluginID: "process", Command: rpc.Command{Path: binary, Args: []string{"duplex"}},
		Identity: processfixture.Identity(), ChildBoundary: processfixture.Boundary("duplex"),
		Limits: rpc.Limits{StartupTimeout: 15 * time.Second, StopTimeout: 15 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func fxClean(t *testing.T, host *gordis.Host, instance *rpc.ProcessPlugin) {
	t.Helper()
	d := instance.Snapshot()
	if !d.Reaped || d.Ready || d.Cleanup == nil || !d.Cleanup.CleanupComplete || d.Cleanup.Forced || len(d.Cleanup.Errors) != 0 {
		t.Fatalf("process cleanup: %+v", d)
	}
	for _, node := range host.Snapshot() {
		if !node.CleanupComplete || node.Leases != 0 || len(node.Residuals) != 0 || node.CleanupError != "" {
			t.Fatalf("Host cleanup: %+v", node)
		}
	}
}

func TestProcessPluginLifetime(t *testing.T) {
	binary := fxBuild(t)
	for _, barrier := range []string{"activate", "handler-cancelled", "callback-cancelled", "cleanup"} {
		t.Run(barrier, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock() // release barriers before t.Cleanup shuts down the Host
			events := make(chan fxEvent, 32)
			attached := make(chan *rpc.ProcessPlugin, 1)
			results := make(chan fxResult, 1)
			callback := &fxControlledCallback{events: events, hook: func(ctx context.Context, input string) error {
				if input == "hold-callback" {
					<-ctx.Done()
					events <- fxEvent{"old", "callback-cancelled"}
					<-release // actual Host handler outlives its cancelled RPC client
				}
				if input == barrier {
					<-release
				}
				return nil
			}}
			host, err := gordis.NewHost(callback, &fxObserved{fxPrototype(t, binary), attached, false}, &fxConsumer{results, false})
			if err != nil {
				t.Fatal(err)
			}
			// Register cleanup before releasing barriers so failures cannot strand children.
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := host.Shutdown(cleanup); err != nil {
					t.Error(err)
				}
			})
			if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "provider", Plugin: "controlled-callback"}); err != nil {
				t.Fatal(err)
			}
			if _, err := host.Env().Mount(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "processfixture/consumer"}); err != nil {
				t.Fatal(err)
			}
			mounted := make(chan error, 1)
			go func() {
				_, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "process", Plugin: "process"})
				mounted <- err
			}()
			if barrier == "activate" {
				fxEventUntil(t, ctx, events, barrier)
				select {
				case err := <-mounted:
					t.Fatalf("Ready before initialization callback returned: %v", err)
				case <-results:
					t.Fatal("consumer activated before initialization returned")
				default:
				}
				unblock()
			}
			if err := fxReceive(t, ctx, mounted); err != nil {
				t.Fatal(err)
			}
			instance := fxReceive(t, ctx, attached)
			result := fxReceive(t, ctx, results)
			var callDone chan error
			if barrier == "handler-cancelled" {
				callDone = make(chan error, 1)
				go func() { _, err := result.text.Call(ctx, "hold-handler"); callDone <- err }()
				fxEventUntil(t, ctx, events, "handler-enter")
			}
			if barrier == "callback-cancelled" {
				callCtx, cancelCall := context.WithCancel(ctx)
				defer cancelCall()
				returned := make(chan error, 1)
				go func() { _, err := result.text.Call(callCtx, "hold-callback"); returned <- err }()
				fxEventUntil(t, ctx, events, "hold-callback")
				cancelCall()
				if err := fxReceive(t, ctx, returned); err == nil {
					t.Fatal("cancelled callback call succeeded")
				}
				fxEventUntil(t, ctx, events, "callback-cancelled")
			}
			stopped := make(chan error, 1)
			go func() { stopped <- host.Shutdown(ctx) }()
			if barrier != "activate" {
				expected := barrier
				if barrier == "callback-cancelled" {
					expected = "cleanup" // child reached cleanup while Host callback is still blocked
				}
				seen := fxEventUntil(t, ctx, events, expected)
				for _, event := range seen {
					if event.input == "provider-cleanup" || barrier == "handler-cancelled" && event.input == "cleanup" {
						t.Fatalf("cleanup ran while actual handler was blocked: %v", seen)
					}
				}
				if d := instance.Snapshot(); barrier != "callback-cancelled" && (d.Reaped || d.Cleanup != nil) {
					t.Fatalf("process released before actual %s returned: %+v", barrier, d)
				}
				for _, node := range host.Snapshot() {
					if (node.LocalID == "provider" || node.LocalID == "process") && node.CleanupComplete {
						t.Fatalf("fixed provider released during callback: %+v", node)
					}
				}
				select {
				case err := <-stopped:
					t.Fatalf("Shutdown returned before barrier release: %v", err)
				default:
				}
				unblock()
			}
			if err := fxReceive(t, ctx, stopped); err != nil {
				t.Fatal(err)
			}
			if callDone != nil {
				if err := fxReceive(t, ctx, callDone); err == nil {
					t.Fatal("stopped handler call succeeded")
				}
			}
			fxClean(t, host, instance)
		})
	}
}

func TestProcessPluginProviderReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events := make(chan fxEvent, 32)
	attached := make(chan *rpc.ProcessPlugin, 2)
	results := make(chan fxResult, 2)
	host, err := gordis.NewHost(&fxControlledCallback{events: events}, &fxObserved{fxPrototype(t, fxBuild(t)), attached, false}, &fxConsumer{results, false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	provider, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "provider", Plugin: "controlled-callback"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "process", Plugin: "process"})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "processfixture/consumer"})
	if err != nil {
		t.Fatal(err)
	}
	old, oldResult := fxReceive(t, ctx, attached), fxReceive(t, ctx, results)
	if oldResult.value != "old/request" {
		t.Fatal(oldResult.value)
	}
	if err := provider.Update(ctx, json.RawMessage(`{"version":"new"}`)); err != nil {
		t.Fatal(err)
	}
	for _, instance := range []*gordis.Instance{provider, process, consumer} {
		if err := instance.WaitReady(ctx); err != nil {
			t.Fatal(err)
		}
	}
	next, nextResult := fxReceive(t, ctx, attached), fxReceive(t, ctx, results)
	if nextResult.value != "new/request" || old == next || old.Snapshot().Session.ID == next.Snapshot().Session.ID ||
		old.Snapshot().Session.HostGeneration >= next.Snapshot().Session.HostGeneration || !old.Snapshot().Reaped {
		t.Fatalf("replacement/session isolation: old=%+v new=%+v value=%s", old.Snapshot(), next.Snapshot(), nextResult.value)
	}
	if _, err := oldResult.text.Call(ctx, "stale"); err == nil {
		t.Fatal("old proxy reconnected to replacement session")
	}
	if value, err := nextResult.text.Call(ctx, "after-replacement"); err != nil || value != "new/after-replacement" {
		t.Fatalf("new proxy: %s %v", value, err)
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	fxClean(t, host, next)
	var got []string
	for len(events) > 0 {
		event := <-events
		got = append(got, event.version+"/"+event.input)
	}
	want := "[old/activate old/request old/cleanup old/provider-cleanup new/activate new/request new/after-replacement new/cleanup new/provider-cleanup]"
	if fmt.Sprint(got) != want {
		t.Fatalf("fixed dependency callbacks and cleanup order: %v", got)
	}
}
