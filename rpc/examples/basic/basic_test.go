package basic_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/examples/basic/bindings"
	"github.com/RussellLuo/gordis/rpc/examples/basic/contract"
	"github.com/RussellLuo/gordis/rpc/examples/basic/plugin"
)

// Observe only the runtime activation, not the disposable preflight instance.
type observedPlugin struct {
	*rpc.ProcessPlugin
	attached chan *rpc.ProcessPlugin
}

func (p *observedPlugin) Spec() gordis.PluginSpec {
	spec := p.ProcessPlugin.Spec()
	newInstance := spec.New
	spec.New = func() gordis.Plugin {
		return &observedPlugin{ProcessPlugin: newInstance().(*rpc.ProcessPlugin), attached: p.attached}
	}
	return spec
}

func (p *observedPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	err := p.ProcessPlugin.Activate(ctx, scope)
	p.attached <- p.ProcessPlugin
	return err
}

type result struct {
	greeting string
	service  contract.Greeter
}

type consumer struct{ results chan result }

func (p *consumer) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "consumer", Requires: []gordis.ServiceSpec{contract.Key.Spec()},
		New: func() gordis.Plugin { return &consumer{results: p.results} },
	}
}

func (p *consumer) Activate(ctx context.Context, scope *gordis.Scope) error {
	service, err := scope.Get(contract.Key)
	if err != nil {
		return err
	}
	greeting, err := service.Greet(ctx, "Gordis")
	if err == nil {
		p.results <- result{greeting, service}
	}
	return err
}

func TestLocalProcessParityAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "plugin")
	cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-race", "-o", binary, "./plugin/cmd")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build child: %v\n%s", err, output)
	}
	prototype, err := rpc.NewProcess(rpc.ProcessOptions{
		PluginID: contract.PluginID, Command: rpc.Command{Path: binary},
		Identity:      rpc.Identity{ID: "example/basic/program", Version: "1", Protocol: rpc.Protocol},
		ChildBoundary: rpc.Boundary{Exports: []rpc.Binding{bindings.Greeter}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, config, want string
		fail               bool
	}{
		{name: "no-config", want: "Hello, Gordis!"},
		{name: "empty-object", config: `{}`, want: "Hello, Gordis!"},
		{name: "explicit-config", config: `{"prefix":"Hello"}`, want: "Hello, Gordis!"},
		{name: "configured", config: `{"prefix":"Welcome"}`, want: "Welcome, Gordis!"},
		{name: "empty-prefix", config: `{"prefix":""}`, fail: true},
		{name: "wrong-type", config: `{"prefix":7}`, fail: true},
		{name: "unknown-field", config: `{"unexpected":true}`, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputs := make(map[string]string)
			for _, mode := range []string{"local", "process"} {
				t.Run(mode, func(t *testing.T) {
					callCtx, stop := context.WithTimeout(ctx, 15*time.Second)
					defer stop()
					attached := make(chan *rpc.ProcessPlugin, 1)
					var provider gordis.Plugin = new(plugin.Plugin)
					if mode == "process" {
						provider = &observedPlugin{ProcessPlugin: prototype, attached: attached}
					}
					results := make(chan result, 1)
					host, err := gordis.NewHost(provider, &consumer{results: results})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
						defer stop()
						if err := host.Shutdown(cleanupCtx); err != nil {
							t.Error(err)
						}
					})
					_, err = host.Env().MountReady(callCtx, gordis.InstanceSpec{
						ID: "greeter", Plugin: contract.PluginID, Config: json.RawMessage(tc.config),
					})
					if (err != nil) != tc.fail {
						t.Fatalf("provider Ready: %v, want failure=%v", err, tc.fail)
					}
					var instance *rpc.ProcessPlugin
					var service contract.Greeter
					if mode == "process" {
						select {
						case instance = <-attached:
						case <-callCtx.Done():
							t.Fatal("Attach did not finish", callCtx.Err())
						}
						if instance == nil || instance.Snapshot().PID == 0 {
							t.Fatal("real child was not acquired")
						}
						if tc.fail {
							diagnostics := instance.Snapshot()
							if diagnostics.Ready || !diagnostics.Reaped {
								t.Fatalf("failed Attach returned before reaping: %+v", diagnostics)
							}
						}
					}
					if tc.fail {
						select {
						case got := <-results:
							t.Fatalf("invalid configuration produced a result: %+v", got)
						default:
						}
					}
					if !tc.fail {
						if _, err := host.Env().MountReady(callCtx, gordis.InstanceSpec{ID: "consumer", Plugin: "consumer"}); err != nil {
							t.Fatal(err)
						}
						select {
						case got := <-results:
							outputs[mode], service = got.greeting, got.service
						default:
							t.Fatal("consumer became Ready without a business result")
						}
						if outputs[mode] != tc.want {
							t.Fatalf("greeting: %q, want %q", outputs[mode], tc.want)
						}
					}
					if err := host.Shutdown(callCtx); err != nil {
						t.Fatal(err)
					}
					for _, node := range host.Snapshot() {
						if !node.CleanupComplete || node.CleanupError != "" || node.Leases != 0 || len(node.Residuals) != 0 {
							t.Fatalf("Host cleanup incomplete: %+v", node)
						}
					}
					if instance != nil {
						diagnostics := instance.Snapshot()
						if diagnostics.Ready || !diagnostics.Reaped || diagnostics.Cleanup == nil {
							t.Fatalf("child reaping: %+v", diagnostics)
						}
						if tc.fail && (!diagnostics.Cleanup.Forced || diagnostics.Cleanup.CleanupComplete) {
							t.Fatalf("expected pre-initialize rejection: %+v", diagnostics)
						}
						if !tc.fail && (!diagnostics.Cleanup.CleanupComplete || diagnostics.Cleanup.Forced || len(diagnostics.Cleanup.Errors) != 0) {
							t.Fatalf("child cleanup/reaping: %+v", diagnostics)
						}
						if service != nil {
							if _, err := service.Greet(callCtx, "unavailable"); err == nil {
								t.Fatal("stopped child exposed a callable export")
							}
						}
					}
				})
			}
			if !tc.fail && outputs["local"] != outputs["process"] {
				t.Fatalf("Local/process differ: %v", outputs)
			}
		})
	}
}
