package gordisrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/testfixture"
)

type callbackProvider struct{ service testfixture.Service }

func (p *callbackProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "callback", New: func() gordis.Plugin { return &callbackProvider{p.service} }, Provides: []gordis.ServiceSpec{testfixture.CallbackKey.Spec()}}
}
func (p *callbackProvider) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide(testfixture.CallbackKey, p.service)
}

type loader struct {
	options rpc.AttachOptions
	opened  func(*rpc.Session)
}

func (p *loader) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "loader", New: func() gordis.Plugin { return &loader{p.options, p.opened} }, Requires: rpc.RequiredServices(p.options.ChildBoundary.Imports)}
}
func (p *loader) Activate(ctx context.Context, scope *gordis.Scope) error {
	session, err := rpc.Attach(ctx, scope, p.options)
	p.opened(session)
	return err
}
func await(t *testing.T, ctx context.Context, events <-chan string, expected string) {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event == expected {
				return
			}
		case <-ctx.Done():
			t.Fatalf("missing %s: %v", expected, ctx.Err())
		}
	}
}
func TestProcessLifecycle(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "plugin")
	if output, err := exec.Command("go", "build", "-race", "-o", binary, "./testdata/plugin").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, mode := range []string{"normal", "discover", "identity-mismatch", "boundary-mismatch", "accept-failure", "activation-failure", "validation-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			events := make(chan string, 32)
			callback := testfixture.Function(func(ctx context.Context, input string) (string, error) {
				select {
				case events <- input:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return "callback/" + input, nil
			})
			options := rpc.AttachOptions{Command: rpc.Command{Path: binary}, Identity: testfixture.Identity(), Config: json.RawMessage(`{}`), ChildBoundary: testfixture.Unit().Boundary()}
			switch mode {
			case "discover":
				options.Identity = rpc.Identity{}
			case "identity-mismatch":
				options.Identity.Version = "wrong"
			case "boundary-mismatch":
				options.ChildBoundary.Exports = nil
			case "accept-failure":
				options.Accept = func(context.Context, *rpc.Session) error { return errors.New("rejected") }
			case "activation-failure":
				options.Config = json.RawMessage(`{"fail_activation":true}`)
			case "validation-cancel":
				options.Config = json.RawMessage(`{"wait_validation":true}`)
			}
			var session *rpc.Session
			host, err := gordis.NewHost(&callbackProvider{callback}, &loader{options, func(value *rpc.Session) { session = value }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
			if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "callback", Plugin: "callback"}); err != nil {
				t.Fatal(err)
			}
			startup, stopStartup := context.WithCancel(ctx)
			defer stopStartup()
			completed := make(chan error, 1)
			go func() {
				_, err := host.Env().MountReady(startup, gordis.InstanceSpec{ID: "process", Plugin: "loader"})
				completed <- err
			}()
			if mode == "validation-cancel" {
				await(t, ctx, events, "validating")
				stopStartup()
			}
			select {
			case err = <-completed:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Wait for activation rollback as MountReady may return immediately on cancellation.
			if mode != "normal" && mode != "discover" {
				if err == nil {
					t.Fatal("failed initialization became Ready")
				}
				if err = host.Shutdown(ctx); err != nil && mode != "validation-cancel" {
					t.Fatal(err)
				}
				if session == nil || !session.Snapshot().Reaped {
					t.Fatalf("failed process not reaped: %v", session)
				}
				if mode == "identity-mismatch" || mode == "boundary-mismatch" {
					select {
					case event := <-events:
						t.Fatalf("business ran before handshake validation: %s", event)
					default:
					}
				} else {
					await(t, ctx, events, "cleanup")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if session.Snapshot().Identity != testfixture.Identity() {
				t.Fatal("discovered identity missing")
			}
			service, err := rpc.ExportClient(session, testfixture.Key)
			if err != nil {
				t.Fatal(err)
			}
			output, err := service.Call(ctx, "hello")
			if err != nil || output != "callback/hello" {
				t.Fatalf("call: %q %v", output, err)
			}
			stopped := errors.New("consumer stopped")
			err = service.Stream(ctx, func(json.RawMessage) error { return stopped })
			if !errors.Is(err, stopped) {
				t.Fatalf("stream: %v", err)
			}
			await(t, ctx, events, "stream-stopped")
			if err = host.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			diagnostics := session.Snapshot()
			if !diagnostics.Reaped || diagnostics.Ready || diagnostics.Cleanup == nil || !diagnostics.Cleanup.CleanupComplete || diagnostics.Cleanup.Forced {
				t.Fatalf("cleanup: %+v", diagnostics)
			}
			if _, err = service.Call(ctx, "stale"); err == nil {
				t.Fatal("closed session accepted a call")
			}
			await(t, ctx, events, "cleanup")
		})
	}
}
