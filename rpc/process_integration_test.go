package gordisrpc_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture/contract"
)

// Observation and fault injection live in the tests, outside ProcessOptions.
type fxObserved struct {
	*rpc.ProcessPlugin
	attached   chan *rpc.ProcessPlugin
	omitNumber bool
}

func (p *fxObserved) Spec() gordis.PluginSpec {
	spec := p.ProcessPlugin.Spec()
	newInstance := spec.New
	if p.omitNumber {
		spec.Provides = spec.Provides[:1]
	}
	spec.New = func() gordis.Plugin { return &fxObserved{newInstance().(*rpc.ProcessPlugin), p.attached, p.omitNumber} }
	return spec
}
func (p *fxObserved) Activate(ctx context.Context, scope *gordis.Scope) error {
	err := p.ProcessPlugin.Activate(ctx, scope)
	p.attached <- p.ProcessPlugin
	return err
}

type fxCallback struct{ events chan string }

func (p *fxCallback) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "processfixture/callback", New: func() gordis.Plugin { return &fxCallback{p.events} }, Provides: []gordis.ServiceSpec{contract.CallbackKey.Spec()}}
}
func (p *fxCallback) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("provider cleanup", func(context.Context) error { p.events <- "provider-cleanup"; return nil }); err != nil {
		return err
	}
	return scope.Provide(contract.CallbackKey, contract.Text(p))
}
func (p *fxCallback) Call(_ context.Context, input string) (string, error) {
	p.events <- input
	return "callback/" + input, nil
}

type fxResult struct {
	text   contract.Text
	value  string
	number int
}
type fxConsumer struct {
	results chan fxResult
	multi   bool
}

func (p *fxConsumer) Spec() gordis.PluginSpec {
	requires := []gordis.ServiceSpec{contract.TextKey.Spec()}
	if p.multi {
		requires = append(requires, contract.NumberKey.Spec())
	}
	return gordis.PluginSpec{ID: "processfixture/consumer", New: func() gordis.Plugin { return &fxConsumer{p.results, p.multi} }, Requires: requires}
}
func (p *fxConsumer) Activate(ctx context.Context, scope *gordis.Scope) error {
	text, err := scope.Get(contract.TextKey)
	if err != nil {
		return err
	}
	value, err := text.Call(ctx, "request")
	if err != nil {
		return err
	}
	result := fxResult{text: text, value: value}
	if p.multi {
		number, err := scope.Get(contract.NumberKey)
		if err != nil {
			return err
		}
		result.number, err = number.Value(ctx)
		if err != nil {
			return err
		}
	}
	p.results <- result
	return nil
}

func TestProcessPluginBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "child")
	if output, err := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "./testdata/process").CombinedOutput(); err != nil {
		t.Fatalf("build child: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		name, mode, config, want string
		fail, omit               bool
	}{
		{name: "no-import-single-export", mode: "single", want: "Hello/request"},
		{name: "empty-forward-config", mode: "single", config: `{}`, want: "Hello/request"},
		{name: "forward-business-object", mode: "single", config: ` { "prefix" : "Welcome" } `, want: "Welcome/request"},
		{name: "multiple-exports", mode: "multi", want: "Hello/request"},
		{name: "duplex", mode: "duplex", want: "callback/request"},
		{name: "import-only-no-config", mode: "import-only"},
		{name: "import-only-explicit-empty", mode: "import-only", config: `{}`},
		{name: "task-no-config", mode: "task"},
		{name: "invalid-provider-config", mode: "single", config: `{"prefix":""}`, fail: true},
		{name: "typed-nil-second-client", mode: "multi", fail: true},
		{name: "nil-second-client", mode: "multi", fail: true},
		{name: "second-provide-fails", mode: "multi", fail: true, omit: true},
		{name: "no-config-rejects-fields", mode: "import-only", config: `{"prefix":"ignored"}`, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			boundary := processfixture.Boundary(tc.mode)
			var textCalls, numberCalls atomic.Int32
			var created contract.Text
			if len(boundary.Exports) != 0 {
				codec := processfixture.TextCodec()
				factory := codec.Client
				codec.Client = func(invoker rpc.Invoker) contract.Text { textCalls.Add(1); created = factory(invoker); return created }
				boundary.Exports[0] = rpc.NewBinding(contract.TextKey, "1", codec)
			}
			if tc.mode == "multi" {
				codec := processfixture.NumberCodec()
				factory := codec.Client
				codec.Client = func(invoker rpc.Invoker) contract.Number {
					numberCalls.Add(1)
					if tc.name == "typed-nil-second-client" {
						return (*processfixture.NumberClient)(nil)
					}
					if tc.name == "nil-second-client" {
						return nil
					}
					return factory(invoker)
				}
				boundary.Exports[1] = rpc.NewBinding(contract.NumberKey, "1", codec)
			}
			options := rpc.ProcessOptions{PluginID: "processfixture/process", Command: rpc.Command{Path: binary, Args: []string{tc.mode}}, Identity: processfixture.Identity(), ChildBoundary: boundary}
			prototype, err := rpc.NewProcess(options)
			if err != nil {
				t.Fatal(err)
			}
			if len(prototype.Spec().Requires) != len(boundary.Imports) || len(prototype.Spec().Provides) != len(boundary.Exports) {
				t.Fatal("Boundary not reflected in Spec")
			}
			attached := make(chan *rpc.ProcessPlugin, 1)
			events := make(chan string, 16)
			results := make(chan fxResult, 1)
			// Failure cases deliberately use a consumer requiring only the FIRST
			// export, so a leaked partial publication would activate it.
			host, err := gordis.NewHost(&fxObserved{prototype, attached, tc.omit}, &fxCallback{events}, &fxConsumer{results, tc.mode == "multi" && !tc.fail})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := host.Shutdown(cleanup); err != nil {
					t.Error(err)
				}
			})
			if len(boundary.Imports) != 0 {
				if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "callback", Plugin: "processfixture/callback"}); err != nil {
					t.Fatal(err)
				}
			}
			if len(boundary.Exports) != 0 {
				if _, err = host.Env().Mount(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "processfixture/consumer"}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "process", Plugin: "processfixture/process", Config: json.RawMessage(tc.config)})
			if (err != nil) != tc.fail {
				t.Fatalf("Ready: %v, want failure=%v", err, tc.fail)
			}
			var instance *rpc.ProcessPlugin
			select {
			case instance = <-attached:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			d := instance.Snapshot()
			if d.PID == 0 {
				t.Fatal("no real child")
			}
			if tc.fail {
				if !d.Reaped || d.Ready {
					t.Fatalf("failed activation returned before reap: %+v", d)
				}
				if tc.name == "no-config-rejects-fields" {
					if !strings.Contains(err.Error(), "program config schema") {
						t.Fatal(err)
					}
					if d.Cleanup == nil || !d.Cleanup.Forced {
						t.Fatalf("unexpected pre-initialize cleanup: %+v", d)
					}
					select {
					case event := <-events:
						t.Fatalf("business activated: %s", event)
					default:
					}
					return
				}
				if strings.Contains(tc.name, "nil-second-client") && !strings.Contains(err.Error(), "invalid contract client") {
					t.Fatalf("wrong failure: %v", err)
				}
				if tc.omit && !strings.Contains(err.Error(), "provide") {
					t.Fatalf("wrong Provide failure: %v", err)
				}
			} else if !d.Ready || d.Reaped {
				t.Fatalf("not Ready: %+v", d)
			}
			var result fxResult
			if !tc.fail && len(boundary.Exports) != 0 {
				select {
				case result = <-results:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if result.value != tc.want || result.text != created {
					t.Fatalf("wrong result/client: %+v", result)
				}
				if tc.mode == "multi" && result.number != 42 {
					t.Fatal("second export not usable")
				}
			}
			if textCalls.Load() != 0 && textCalls.Load() != 1 || numberCalls.Load() != 0 && numberCalls.Load() != 1 {
				t.Fatal("factory invoked more than once")
			}
			if !tc.fail || tc.mode == "multi" {
				if int(textCalls.Load()) != min(1, len(boundary.Exports)) || int(numberCalls.Load()) != max(0, len(boundary.Exports)-1) {
					t.Fatal("not every client was constructed exactly once")
				}
			}
			if err := host.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			d = instance.Snapshot()
			if !d.Reaped || d.Ready || d.Cleanup == nil || !d.Cleanup.CleanupComplete || d.Cleanup.Forced {
				t.Fatalf("incomplete child cleanup: %+v", d)
			}
			for _, node := range host.Snapshot() {
				if !node.CleanupComplete || node.Leases != 0 || len(node.Residuals) != 0 || node.CleanupError != "" {
					t.Fatalf("Host cleanup: %+v", node)
				}
			}
			if tc.fail {
				select {
				case <-results:
					t.Fatal("consumer saw partial Ready")
				default:
				}
			}
			if result.text != nil {
				if _, err := result.text.Call(ctx, "stale"); err == nil {
					t.Fatal("closed proxy still callable")
				}
			}
			if len(boundary.Imports) != 0 {
				var got []string
				for len(events) > 0 {
					got = append(got, <-events)
				}
				want := "activate,cleanup,provider-cleanup"
				if tc.mode == "duplex" {
					want = "activate,request,cleanup,provider-cleanup"
				}
				if strings.Join(got, ",") != want {
					t.Fatalf("callback/cleanup order: %v", got)
				}
			}
		})
	}
}
