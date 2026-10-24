package rpc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
)

func TestPreparedExportsSelection(t *testing.T) {
	for _, mode := range []string{"selected", "empty", "unknown", "duplicate", "foreign", "partial-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var count atomic.Int32
			var prepared *PreparedExports
			var private testService
			pub := gordis.NewKey[testService]("selected/public")
			priv := gordis.NewKey[testService]("selected/private")
			binding := func(key gordis.Key[testService]) Binding {
				b := testServiceBinding(key)
				b.client = func(Invoker) any {
					count.Add(1)
					return &testServiceClient{testServiceFunc(func(_ context.Context, msg string) (string, error) { return msg, nil })}
				}
				return b
			}
			plugin := &testPlugin{spec: gordis.PluginSpec{ID: "selection", Provides: []gordis.ServiceSpec{pub.Spec()}}, activate: func(_ context.Context, scope *gordis.Scope) error {
				session := &Session{scope: scope, info: SessionInfo{HostGeneration: scope.Generation()}, remote: &remote{}, boundary: Boundary{Exports: []Binding{binding(pub), binding(priv)}}}
				session.diagnostics.Ready = true
				var err error
				prepared, err = session.PrepareExports()
				if err != nil {
					return err
				}
				private, err = PreparedClient(prepared, priv)
				if err != nil {
					return err
				}
				again, err := PreparedClient(prepared, priv)
				if err != nil || again != private || count.Load() != 2 {
					return errors.New("changed prepared client")
				}
				switch mode {
				case "selected":
					err = prepared.ProvideServices(scope, pub.Spec())
				case "empty":
					err = prepared.ProvideServices(scope)
				case "unknown":
					err = prepared.ProvideServices(scope, gordis.NewKey[testService]("unknown").Spec())
				case "duplicate":
					err = prepared.ProvideServices(scope, pub.Spec(), pub.Spec())
				case "foreign":
					err = prepared.ProvideServices(&gordis.Scope{}, pub.Spec())
				case "partial-failure":
					err = prepared.ProvideServices(scope, pub.Spec(), priv.Spec())
				}
				if mode == "selected" && err != nil {
					return err
				}
				if mode != "selected" && mode != "empty" && err == nil {
					return errors.New("invalid selection accepted")
				}
				if prepared.Provide(scope) == nil {
					return errors.New("publication opportunity reused")
				}
				if mode == "empty" {
					return errors.New("intentional empty-publication rollback")
				}
				return err
			}}
			host, err := gordis.NewHost(plugin)
			requireNoError(t, err)
			defer host.Shutdown(context.Background())
			_, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "selection", Plugin: "selection"})
			if (mode == "selected") != (err == nil) {
				t.Fatal(mode, err)
			}
			if count.Load() != 2 {
				t.Fatal("factory repeated", count.Load())
			}
			requireNoError(t, host.Shutdown(ctx))
		})
	}
}

type testServiceClient struct{ testService }

func TestPrepareExportsCachesClientsAndErrors(t *testing.T) {
	for _, mode := range []string{"success", "nil", "typed-nil", "wrong-type", "panic", "second-nil"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			client := &testServiceClient{testServiceFunc(func(_ context.Context, input string) (string, error) { return input, nil })}
			binding := testServiceBinding(testExportKey)
			binding.client = func(Invoker) any {
				calls.Add(1)
				switch mode {
				case "nil":
					return nil
				case "typed-nil":
					return (*testServiceClient)(nil)
				case "wrong-type":
					return 42
				case "panic":
					panic("factory failure")
				}
				return client
			}
			session := &Session{remote: &remote{}, boundary: Boundary{Exports: []Binding{binding}}}
			session.diagnostics.Ready = true
			if mode == "second-nil" {
				second := testServiceBinding(gordis.NewKey[testService]("test/second-export"))
				second.client = func(Invoker) any { return nil }
				session.boundary.Exports = append(session.boundary.Exports, second)
			}
			var workers sync.WaitGroup
			results := make(chan *PreparedExports, 16)
			errs := make(chan error, 16)
			for range 16 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					prepared, err := session.PrepareExports()
					results <- prepared
					errs <- err
				}()
			}
			workers.Wait()
			prepared, err := session.PrepareExports()
			if (mode == "success") != (err == nil) || calls.Load() != 1 {
				t.Fatalf("prepare: %v; factory calls: %d", err, calls.Load())
			}
			for range 16 {
				if <-results != prepared || <-errs != err {
					t.Fatal("preparation result was not cached")
				}
			}
			if err != nil {
				if prepared != nil {
					t.Fatal("partially prepared exports escaped")
				}
				return
			}
			observed, err := PreparedClient(prepared, testExportKey)
			requireNoError(t, err)
			if observed != client {
				t.Fatal("prepared client identity changed")
			}
			if _, err := PreparedClient(prepared, gordis.NewKey[int](testExportKey.Name())); err == nil {
				t.Fatal("wrong typed key accepted")
			}
			if _, err := PreparedClient(prepared, testImportKey); err == nil {
				t.Fatal("undeclared export accepted")
			}
		})
	}
}

func TestPreparedExportsRejectsClosedSession(t *testing.T) {
	var absent *Session
	if _, err := absent.PrepareExports(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	for _, mode := range []string{"not-ready", "no-remote", "stopping", "closed-after-prepare"} {
		t.Run(mode, func(t *testing.T) {
			session := &Session{remote: &remote{}}
			session.diagnostics.Ready = mode != "not-ready"
			session.stopping = mode == "stopping"
			if mode == "no-remote" {
				session.remote = nil
			}
			prepared, err := session.PrepareExports()
			if mode != "closed-after-prepare" {
				if !errors.Is(err, gordis.ErrClosed) || prepared != nil {
					t.Fatalf("inactive session prepared exports: %v", err)
				}
				return
			}
			requireNoError(t, err)
			session.stopping = true
			if _, err := session.PrepareExports(); !errors.Is(err, gordis.ErrClosed) {
				t.Fatal(err)
			}
			if err := prepared.Provide(&gordis.Scope{}); !errors.Is(err, gordis.ErrClosed) {
				t.Fatal(err)
			}
		})
	}
}
