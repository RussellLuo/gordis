package gordisrpc_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
)

func TestProcessPluginConcurrentCallbacks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	const count = 4
	entered := make(chan string, count)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	callback := &fxControlledCallback{events: make(chan fxEvent, 32), hook: func(ctx context.Context, input string) error {
		if !strings.HasPrefix(input, "request-") {
			return nil
		}
		entered <- input
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	attached := make(chan *rpc.ProcessPlugin, 1)
	services := make(chan fxResult, 1)
	host, err := gordis.NewHost(callback, &fxObserved{fxPrototype(t, fxBuild(t)), attached, false}, &fxConsumer{services, false})
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
	for _, instance := range []gordis.InstanceSpec{
		{ID: "provider", Plugin: "controlled-callback"},
		{ID: "process", Plugin: "process"},
		{ID: "consumer", Plugin: "processfixture/consumer"},
	} {
		if _, err := host.Env().MountReady(ctx, instance); err != nil {
			t.Fatal(err)
		}
	}
	instance := fxReceive(t, ctx, attached)
	service := fxReceive(t, ctx, services).text
	completed := make(chan error, count)
	for i := range count {
		go func() {
			input := fmt.Sprintf("request-%d", i)
			value, err := service.Call(ctx, input)
			if err == nil && value != "old/"+input {
				err = fmt.Errorf("response for %s: %q", input, value)
			}
			completed <- err
		}()
	}
	seen := make(map[string]bool)
	for range count {
		input := fxReceive(t, ctx, entered)
		if seen[input] {
			t.Fatalf("duplicate callback: %s", input)
		}
		seen[input] = true
	}
	// Every nested callback is active before any may return; no sleeps or
	// scheduler assumptions are needed to establish overlapping requests.
	unblock()
	for range count {
		if err := fxReceive(t, ctx, completed); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	fxClean(t, host, instance)
}
