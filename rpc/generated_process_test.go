package gordisrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/bindings"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/contract"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
)

type generatedConsumer struct{ services chan contract.Service }

func (p *generatedConsumer) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "genfixture/consumer", New: func() gordis.Plugin { return &generatedConsumer{p.services} }, Requires: []gordis.ServiceSpec{contract.Key.Spec()}}
}
func (p *generatedConsumer) Activate(_ context.Context, s *gordis.Scope) error {
	service, err := s.Get(contract.Key)
	if err == nil {
		p.services <- service
	}
	return err
}

func TestGeneratedUnaryProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "generated-child")
	if output, err := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "./testdata/generated").CombinedOutput(); err != nil {
		t.Fatalf("build child: %v\n%s", err, output)
	}
	for _, old := range []bool{false, true} {
		name := "generated"
		if old {
			name = "old-format-rejected"
		}
		t.Run(name, func(t *testing.T) {
			binding := bindings.Service
			if old {
				binding = rpc.NewBinding(contract.Key, "1", rpc.Codec[contract.Service]{Client: func(rpc.Invoker) contract.Service { return nil }, Server: func(contract.Service) rpc.Handler { return rpc.Handler{} }})
			}
			prototype, err := rpc.NewProcess(rpc.ProcessOptions{PluginID: "genfixture/process", Command: rpc.Command{Path: binary}, Identity: rpc.Identity{ID: "genfixture/program", Version: "1", Protocol: rpc.Protocol}, ChildBoundary: rpc.Boundary{Exports: []rpc.Binding{binding}}})
			if err != nil {
				t.Fatal(err)
			}
			attached := make(chan *rpc.ProcessPlugin, 1)
			services := make(chan contract.Service, 1)
			host, err := gordis.NewHost(&fxObserved{ProcessPlugin: prototype, attached: attached}, &generatedConsumer{services})
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
			if _, err := host.Env().Mount(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "genfixture/consumer"}); err != nil {
				t.Fatal(err)
			}
			config := json.RawMessage(`{"prefix":"Welcome"}`)
			if old {
				// This would fail child config decoding if Initialize ran first.
				config = json.RawMessage(`{"prefix":123}`)
			}
			_, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "process", Plugin: "genfixture/process", Config: config})
			var instance *rpc.ProcessPlugin
			select {
			case instance = <-attached:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if old {
				if err == nil || !strings.Contains(err.Error(), "boundary mismatch") {
					t.Fatalf("old wire was not rejected: %v", err)
				}
				d := instance.Snapshot()
				if !d.Reaped || d.Ready || d.PID == 0 {
					t.Fatalf("handshake failed without reap: %+v", d)
				}
				select {
				case <-services:
					t.Fatal("consumer activated after handshake rejection")
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var service contract.Service
			select {
			case service = <-services:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if instance.Snapshot().PID == 0 {
				t.Fatal("no actual child")
			}
			if err := service.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			if v, err := service.Value(ctx); err != nil || v != 42 {
				t.Fatalf("Value: %d, %v", v, err)
			}
			if v, err := service.Greet(ctx, "Qi", 2); err != nil || v != "Welcome, Qi!Welcome, Qi!" {
				t.Fatalf("Greet: %q, %v", v, err)
			}
			want := dto.Value{Count: 2, Node: &dto.Node{Name: "root", Next: &dto.Node{Name: "leaf"}}, Labels: map[string][]string{"key": {"value"}}, Pair: [2]int{1, 2}, Bytes: []byte{1, 2}, NamedBytes: []dto.Byte{3, 4}, Payload: dto.Raw(`{"name":"Qi","n":9007199254740993}`)}
			if v, err := service.Echo(ctx, want); err != nil || !reflect.DeepEqual(v, want) {
				t.Fatalf("Echo: %+v, %v", v, err)
			}
			if v, err := service.EchoJSON(ctx, want.Payload); err != nil || string(v) != string(want.Payload) {
				t.Fatalf("EchoJSON: %s, %v", v, err)
			}
			var chunks []string
			if err := service.JSONStream(ctx, want.Payload, func(v dto.Raw) error {
				chunks = append(chunks, string(v))
				return nil
			}); err != nil || !reflect.DeepEqual(chunks, []string{string(want.Payload)}) {
				t.Fatalf("JSONStream: %v, %v", chunks, err)
			}
			if v, err := service.Nullable(ctx, nil); err != nil || v != nil {
				t.Fatalf("Nullable: %v, %v", v, err)
			}
			if err := service.Fail(ctx); err == nil || !strings.Contains(err.Error(), "fixture business failure") {
				t.Fatal(err)
			}
			deadline, stop := context.WithTimeout(ctx, 50*time.Millisecond)
			if err := service.Wait(deadline); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			stop()
			cancelled, stop := context.WithCancel(ctx)
			stop()
			if err := service.Wait(cancelled); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if err := service.Ping(ctx); err != nil {
				t.Fatalf("session unusable after cancelled call: %v", err)
			}
			if err := host.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			d := instance.Snapshot()
			if !d.Reaped || d.Ready || d.Cleanup == nil || !d.Cleanup.CleanupComplete || d.Cleanup.Forced {
				t.Fatalf("child cleanup: %+v", d)
			}
			for _, node := range host.Snapshot() {
				if !node.CleanupComplete || node.Leases != 0 || len(node.Residuals) != 0 || node.CleanupError != "" {
					t.Fatalf("Host cleanup: %+v", node)
				}
			}
			if err := service.Ping(ctx); err == nil {
				t.Fatal("old proxy still callable")
			}
		})
	}
}
