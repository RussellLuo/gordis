// This example shows a Scope managing a background task, a stop callback,
// an in-flight request lease, and final cleanup. Unmount rejects new requests
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
	scopeReady     chan<- *gordis.Scope
}

func (p *workerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "worker",
		New: func() gordis.Plugin {
			return &workerPlugin{entranceClosed: p.entranceClosed, scopeReady: p.scopeReady}
		},
	}
}

func (p *workerPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
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
	if err := scope.Go("task", func(ctx context.Context) error {
		<-ctx.Done()
		fmt.Println("task stopped")
		return ctx.Err()
	}); err != nil {
		return err
	}
	p.scopeReady <- scope
	return nil
}

func run() error {
	entranceClosed := make(chan struct{})
	scopeReady := make(chan *gordis.Scope, 1)
	worker := &workerPlugin{entranceClosed: entranceClosed, scopeReady: scopeReady}
	h, err := gordis.NewHost(worker)
	if err != nil {
		return err
	}
	defer h.Shutdown(context.Background())

	instance, err := h.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "worker", Plugin: "worker"})
	if err != nil {
		return err
	}
	scope := <-scopeReady

	// Hold one admitted request while unmount begins.
	release, err := scope.Acquire()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- instance.Unmount(ctx) }()

	// New requests are rejected while the admitted request drains.
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
	fmt.Println("stop waits for the request")
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
