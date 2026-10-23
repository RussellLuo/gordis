package gordisrpc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture/contract"
)

func TestProcessPluginInstanceIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prototype, err := rpc.NewProcess(rpc.ProcessOptions{PluginID: "process", Command: rpc.Command{Path: fxBuild(t), Args: []string{"single"}},
		ChildBoundary: processfixture.Boundary("single")}) // zero Identity exercises discovery
	if err != nil {
		t.Fatal(err)
	}
	attached := make(chan *rpc.ProcessPlugin, 2)
	results := make(chan fxResult, 2)
	host, err := gordis.NewHost(&fxObserved{prototype, attached, false}, &fxConsumer{results, false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	var handles []*gordis.Instance
	var instances []*rpc.ProcessPlugin
	var clients []contract.Text
	for _, prefix := range []string{"First", "Second"} {
		env := host.Env().Isolate(contract.TextKey, prefix)
		config, err := json.Marshal(map[string]string{"prefix": prefix})
		if err != nil {
			t.Fatal(err)
		}
		handle, err := env.MountReady(ctx, gordis.InstanceSpec{ID: prefix, Plugin: "process", Config: config})
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
		instances = append(instances, fxReceive(t, ctx, attached))
		if _, err := env.MountReady(ctx, gordis.InstanceSpec{ID: prefix + "-consumer", Plugin: "processfixture/consumer"}); err != nil {
			t.Fatal(err)
		}
		result := fxReceive(t, ctx, results)
		if result.value != prefix+"/request" {
			t.Fatalf("configuration crossed instances: %s", result.value)
		}
		clients = append(clients, result.text)
	}
	a, b := instances[0].Snapshot(), instances[1].Snapshot()
	if a.Identity != processfixture.Identity() || b.Identity != a.Identity || a.PID == b.PID || a.Session.ID == b.Session.ID || a.Session.HostInstance == b.Session.HostInstance {
		t.Fatalf("session state crossed instances: %+v %+v", a, b)
	}
	if err := handles[0].Unmount(ctx); err != nil {
		t.Fatal(err)
	}
	if !instances[0].Snapshot().Reaped || !instances[1].Snapshot().Ready || instances[1].Snapshot().Reaped {
		t.Fatal("stopping one instance affected another")
	}
	if _, err := clients[0].Call(ctx, "stale"); err == nil {
		t.Fatal("closed instance proxy still callable")
	}
	if value, err := clients[1].Call(ctx, "alive"); err != nil || value != "Second/alive" {
		t.Fatalf("second instance: %s %v", value, err)
	}
	if prototype.Snapshot().PID != 0 {
		t.Fatal("prototype acquired instance diagnostics")
	}
	// Snapshot changes cannot modify diagnostics retained by the instance.
	a = instances[0].Snapshot()
	a.Cleanup.Errors = append(a.Cleanup.Errors, "changed")
	a.Nodes[0].LocalID = "changed"
	if d := instances[0].Snapshot(); len(d.Cleanup.Errors) != 0 || d.Nodes[0].LocalID == "changed" {
		t.Fatal("Snapshot exposed mutable diagnostics")
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	fxClean(t, host, instances[1])
}
