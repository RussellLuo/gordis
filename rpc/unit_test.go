package gordisrpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"google.golang.org/grpc"
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
