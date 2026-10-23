package rpc_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture/consumer"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture/contract"
)

type streamCallback struct {
	events chan string
	hook   func(context.Context, string) error
}

func (p *streamCallback) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "streamfixture/callback", New: func() gordis.Plugin { return &streamCallback{p.events, p.hook} }, Provides: []gordis.ServiceSpec{contract.CallbackKey.Spec()}}
}
func (p *streamCallback) Activate(_ context.Context, s *gordis.Scope) error {
	if err := s.Defer("provider cleanup", func(context.Context) error { p.events <- "provider-cleanup"; return nil }); err != nil {
		return err
	}
	return s.Provide(contract.CallbackKey, contract.Callback(p))
}
func (p *streamCallback) Call(ctx context.Context, token string) (string, error) {
	p.events <- token
	if p.hook != nil {
		if err := p.hook(ctx, token); err != nil {
			return "", err
		}
	}
	return "host/" + token, nil
}

func buildStream(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "stream-child")
	if output, err := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "./testdata/stream").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	return binary
}
func streamHost(t *testing.T, binary string, only bool, callback *streamCallback) (*gordis.Host, <-chan *rpc.ProcessPlugin, <-chan consumer.Services) {
	t.Helper()
	var args []string
	if only {
		args = []string{"import-only"}
	}
	prototype, err := rpc.NewProcess(rpc.ProcessOptions{PluginID: "streamfixture/process", Command: rpc.Command{Path: binary, Args: args}, Identity: streamfixture.Identity(), ChildBoundary: streamfixture.Boundary(only), Limits: rpc.Limits{StartupTimeout: 15 * time.Second, StopTimeout: 15 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	attached := make(chan *rpc.ProcessPlugin, 1)
	services := make(chan consumer.Services, 1)
	host, err := gordis.NewHost(callback, &fxObserved{ProcessPlugin: prototype, attached: attached}, &consumer.Plugin{Services: services})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := host.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return host, attached, services
}
func streamEvent(t *testing.T, ctx context.Context, events <-chan string, want string) []string {
	t.Helper()
	var seen []string
	for {
		v := fxReceive(t, ctx, events)
		seen = append(seen, v)
		if v == want {
			return seen
		}
	}
}
func TestGeneratedStreamProcess(t *testing.T) {
	binary := buildStream(t)
	for _, only := range []bool{false, true} {
		name := "duplex-multiple-exports"
		if only {
			name = "import-only"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			events := make(chan string, 64)
			callback := &streamCallback{events: events, hook: func(ctx context.Context, token string) error {
				if token == "activate" {
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}}
			host, attached, services := streamHost(t, binary, only, callback)
			if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "provider", Plugin: "streamfixture/callback"}); err != nil {
				t.Fatal(err)
			}
			if !only {
				if _, err := host.Env().Mount(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "streamfixture/consumer"}); err != nil {
					t.Fatal(err)
				}
			}
			mounted := make(chan error, 1)
			go func() {
				_, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "process", Plugin: "streamfixture/process"})
				mounted <- err
			}()
			streamEvent(t, ctx, events, "activate")
			select {
			case err := <-mounted:
				t.Fatalf("Ready before callback returned: %v", err)
			case <-services:
				t.Fatal("consumer saw early exports")
			default:
			}
			unblock()
			if err := fxReceive(t, ctx, mounted); err != nil {
				t.Fatal(err)
			}
			instance := fxReceive(t, ctx, attached)
			if instance.Snapshot().PID == 0 {
				t.Fatal("no child")
			}
			if !only {
				service := fxReceive(t, ctx, services)
				var got []int
				if err := service.Generator.Run(ctx, "finite", 3, func(v dto.Value) error {
					got = append(got, v.Count)
					if v.Node.Name != "finite" {
						t.Fatal(v)
					}
					return nil
				}); err != nil || !reflect.DeepEqual(got, []int{0, 1, 2}) {
					t.Fatalf("finite: %v %v", got, err)
				}
				if err := service.Inspector.Wait(ctx, "finite"); err != nil {
					t.Fatal(err)
				}
				if err := service.Generator.Run(ctx, "empty", 0, func(dto.Value) error { t.Fatal("nonempty stream"); return nil }); err != nil {
					t.Fatal(err)
				}
				var nullable []*dto.Node
				if err := service.Generator.Nullable(ctx, func(v *dto.Node) error { nullable = append(nullable, v); return nil }); err != nil || len(nullable) != 2 || nullable[0] != nil || nullable[1].Name != "last" {
					t.Fatalf("nullable %v %v", nullable, err)
				}
				stop := errors.New("local yield sentinel")
				got = nil
				if err := service.Generator.Run(ctx, "sentinel", -1, func(v dto.Value) error { got = append(got, v.Count); return stop }); err != stop || len(got) != 1 {
					t.Fatalf("sentinel: %v %v", got, err)
				}
				if err := service.Inspector.Wait(ctx, "sentinel"); err != nil {
					t.Fatal(err)
				}
				got = nil
				if err := service.Generator.Run(ctx, "fail", 2, func(v dto.Value) error { got = append(got, v.Count); return nil }); err == nil || !strings.Contains(err.Error(), "stream business failure") || len(got) != 2 {
					t.Fatalf("partial failure: %v %v", got, err)
				}
				for _, mode := range []string{"cancel", "deadline"} {
					callCtx, stopCall := context.WithCancel(ctx)
					if mode == "deadline" {
						stopCall()
						callCtx, stopCall = context.WithTimeout(ctx, 100*time.Millisecond)
					}
					received := make(chan struct{}, 1)
					done := make(chan error, 1)
					go func() {
						done <- service.Generator.Run(callCtx, mode, 1, func(dto.Value) error { received <- struct{}{}; return nil })
					}()
					fxReceive(t, ctx, received)
					if mode == "cancel" {
						stopCall()
					}
					err := fxReceive(t, ctx, done)
					stopCall()
					want := context.Canceled
					if mode == "deadline" {
						want = context.DeadlineExceeded
					}
					if !errors.Is(err, want) {
						t.Fatalf("%s: %v", mode, err)
					}
					if err := service.Inspector.Wait(ctx, mode); err != nil {
						t.Fatal(err)
					}
				}
				if err := service.Generator.Run(ctx, "after-cancel", 1, func(dto.Value) error { return nil }); err != nil {
					t.Fatalf("session not reusable: %v", err)
				}
				if err := host.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
				if err := service.Generator.Run(ctx, "stale", 1, func(dto.Value) error { return nil }); err == nil {
					t.Fatal("stale proxy callable")
				}
			} else {
				if err := host.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			}
			fxClean(t, host, instance)
			seen := streamEvent(t, ctx, events, "provider-cleanup")
			cleanup := -1
			for i, event := range seen {
				if event == "cleanup" {
					cleanup = i
				}
			}
			if cleanup < 0 || seen[len(seen)-1] != "provider-cleanup" {
				t.Fatalf("fixed callback cleanup ordering: %v", seen)
			}
		})
	}
}
func TestGeneratedStreamLifetime(t *testing.T) {
	binary := buildStream(t)
	for _, mode := range []string{"hold", "callback"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			events := make(chan string, 32)
			callback := &streamCallback{events: events, hook: func(ctx context.Context, token string) error {
				if mode == "callback" && token == "run/callback" {
					<-ctx.Done()
					events <- "callback-cancelled"
					<-release
					return ctx.Err()
				}
				if token == "handler-cancelled" {
					<-release
				}
				return nil
			}}
			host, attached, services := streamHost(t, binary, false, callback)
			for _, spec := range []gordis.InstanceSpec{{ID: "provider", Plugin: "streamfixture/callback"}, {ID: "process", Plugin: "streamfixture/process"}, {ID: "consumer", Plugin: "streamfixture/consumer"}} {
				if _, err := host.Env().MountReady(ctx, spec); err != nil {
					t.Fatal(err)
				}
			}
			instance := fxReceive(t, ctx, attached)
			service := fxReceive(t, ctx, services)
			callCtx, stopCall := context.WithCancel(ctx)
			defer stopCall()
			chunk := make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() {
				done <- service.Generator.Run(callCtx, mode, 1, func(dto.Value) error { chunk <- struct{}{}; return nil })
			}()
			if mode == "hold" {
				fxReceive(t, ctx, chunk)
			} else {
				streamEvent(t, ctx, events, "run/callback")
			}
			stopped := make(chan error, 1)
			if mode == "hold" {
				// Shutdown cancels the actual child handler. Its callback
				// barrier proves stop has begun before checking cleanup.
				go func() { stopped <- host.Shutdown(ctx) }()
			} else {
				stopCall()
			}
			if mode == "callback" {
				if err := fxReceive(t, ctx, done); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			barrier := "handler-cancelled"
			if mode == "callback" {
				barrier = "callback-cancelled"
			}
			streamEvent(t, ctx, events, barrier)
			if mode == "callback" {
				go func() { stopped <- host.Shutdown(ctx) }()
			}

			if mode == "callback" {
				streamEvent(t, ctx, events, "cleanup")
			}
			for _, node := range host.Snapshot() {
				if (node.LocalID == "provider" || node.LocalID == "process") && node.CleanupComplete {
					t.Fatalf("dependency released with blocked handler: %+v", node)
				}
			}
			if mode == "hold" && instance.Snapshot().Reaped {
				t.Fatal("child reaped with blocked handler")
			}
			select {
			case err := <-stopped:
				t.Fatalf("shutdown before actual handler exit: %v", err)
			default:
			}
			unblock()
			if err := fxReceive(t, ctx, stopped); err != nil {
				t.Fatal(err)
			}
			if mode == "hold" {
				if err := fxReceive(t, ctx, done); err == nil {
					t.Fatal("stopped stream succeeded")
				}
			}
			fxClean(t, host, instance)
		})
	}
}
