// This example shows a Scope managing a background task, a stop callback,
// an in-flight request lease, and final cleanup. Stop rejects new requests
// and waits for the lease before running Defer.
//
// Run from the repository root: go run ./examples/lifecycle
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/RussellLuo/gordis"
)

type workerPlugin struct {
	entranceClosed chan struct{}
	scope          **gordis.Scope
}

func (p *workerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "worker",
		New: func() gordis.Plugin {
			return &workerPlugin{entranceClosed: p.entranceClosed, scope: p.scope}
		},
	}
}

func (p *workerPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	*p.scope = scope
	if err := scope.Defer("resource", func(context.Context) error {
		fmt.Println("resource released")
		return nil
	}); err != nil {
		return err
	}
	if err := scope.OnStop("entrance", func(context.Context) error {
		fmt.Println("entrance closed")
		close(p.entranceClosed)
		return nil
	}); err != nil {
		return err
	}
	return scope.Go("task", func(ctx context.Context) error {
		<-ctx.Done()
		fmt.Println("task stopped")
		return ctx.Err()
	})
}

func run() error {
	entranceClosed := make(chan struct{})
	var scope *gordis.Scope
	worker := &workerPlugin{entranceClosed: entranceClosed, scope: &scope}
	h, err := gordis.NewHost([]gordis.Plugin{worker}, []gordis.InstanceSpec{{ID: "worker", Plugin: "worker"}})
	if err != nil {
		return err
	}
	if err := h.Start(context.Background()); err != nil {
		return err
	}
	release, err := scope.Acquire() // An in-flight request holds this generation.
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- h.Stop(ctx) }()
	select {
	case <-entranceClosed:
	case <-ctx.Done():
		release()
		return ctx.Err()
	}
	if _, err := scope.Acquire(); !errors.Is(err, gordis.ErrClosed) {
		release()
		return fmt.Errorf("new request: got %v, want ErrClosed", err)
	}
	fmt.Println("new request rejected")
	select {
	case err := <-stopped:
		release()
		return fmt.Errorf("stop finished before the request: %v", err)
	default:
		fmt.Println("stop waits for the request")
	}
	release()
	fmt.Println("request finished")
	if err := <-stopped; err != nil {
		return err
	}
	fmt.Println("stop complete")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
