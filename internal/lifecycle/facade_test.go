package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/internal/lifecycle"
)

type startFunc func(context.Context, *gordis.Scope) error

func (f startFunc) Start(ctx context.Context, scope *gordis.Scope) error { return f(ctx, scope) }

// This package has no access to root or lifecycle private fields. It exercises
// the exact type boundary a future framework subpackage will use, without
// creating another Host or exposing control methods on the plugin facade.
func TestCrossPackageScopeAndFixedBindings(t *testing.T) {
	key := gordis.NewKey[*int]("value")
	value := 42
	bindings := map[string]any{"value": &value}
	requires := []gordis.ServiceSpec{key.Spec()}
	run := lifecycle.New("external-id", 17, requires, requires, bindings, nil)
	delete(bindings, "value")
	requires[0] = gordis.NewKey[string]("mutated").Spec()
	var events []string
	plugin := startFunc(func(_ context.Context, s *gordis.Scope) error {
		if s.ID() != "external-id" || s.Generation() != 17 {
			t.Fatal("identity changed")
		}
		bound, err := gordis.Get(s, key)
		if err != nil || bound != &value {
			t.Fatalf("binding: %v %v", bound, err)
		}
		if _, err := gordis.Get(s, gordis.NewKey[int]("value")); err == nil {
			t.Fatal("wrong Go type accepted")
		}
		if err := gordis.Provide(s, key, (*int)(nil)); err == nil {
			t.Fatal("typed nil accepted")
		}
		if err := gordis.Provide(s, gordis.NewKey[int]("value"), 1); err == nil {
			t.Fatal("wrong provided type accepted")
		}
		if err := s.OnStop("entrance", func(context.Context) error {
			if s.Context().Err() != nil {
				t.Error("cancellation overtook OnStop")
			}
			events = append(events, "entrance")
			return nil
		}); err != nil {
			return err
		}
		if err := s.Defer("first", func(context.Context) error { events = append(events, "first"); return nil }); err != nil {
			return err
		}
		if err := s.Defer("last", func(context.Context) error {
			if *bound != 42 {
				t.Error("captured binding lost during cleanup")
			}
			events = append(events, "last")
			return nil
		}); err != nil {
			return err
		}
		return gordis.Provide(s, key, bound)
	})
	if err := plugin.Start(context.Background(), run.Scope()); err != nil {
		t.Fatal(err)
	}
	if err, _ := run.Commit(context.Background(), nil, nil, func(values map[string]any) {
		if values["value"] != &value {
			t.Error("wrong published object")
		}
		delete(values, "value")
	}); err != nil {
		t.Fatal(err)
	}
	if err := gordis.Provide(run.Scope(), key, &value); err == nil {
		t.Fatal("published scope accepted service")
	}
	release, err := run.Scope().Acquire()
	if err != nil {
		t.Fatal(err)
	}
	run.Freeze()
	if _, err := gordis.Get(run.Scope(), key); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := run.Scope().Acquire(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	run.Quiesce()
	done := make(chan error, 1)
	go func() { done <- run.Dispose() }()
	select {
	case <-run.Scope().Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("not draining")
	}
	select {
	case <-done:
		t.Fatal("cleanup overtook lease")
	default:
	}
	release()
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not finish")
	}
	if !reflect.DeepEqual(events, []string{"entrance", "last", "first"}) {
		t.Fatal(events)
	}
	if run.Retains() {
		t.Fatal("clean generation retained")
	}
	snapshot := run.Snapshot()
	if !snapshot.CleanupComplete || snapshot.Leases != 0 || snapshot.CleanupPhase != "complete" {
		t.Fatal(snapshot)
	}
}

func TestCommitRejectsFailedIncompleteOrRevokedStart(t *testing.T) {
	key := gordis.NewKey[int]("value")
	for _, mode := range []string{"missing", "factory", "task", "frozen", "cancelled", "revoked", "success"} {
		t.Run(mode, func(t *testing.T) {
			failure := make(chan error, 1)
			run := lifecycle.New("plugin", 8, nil, []gordis.ServiceSpec{key.Spec()}, nil, func(err error) { failure <- err })
			defer func() { run.Freeze(); run.Quiesce(); _ = run.Dispose() }()
			if mode != "missing" {
				if err := gordis.Provide(run.Scope(), key, 42); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var startErr error
			switch mode {
			case "factory":
				startErr = lifecycle.Invoke(func() error { panic("factory panic") })
			case "task":
				if err := run.Scope().Go("broken", func(context.Context) error { return errors.New("task failure") }); err != nil {
					t.Fatal(err)
				}
				select {
				case <-failure:
				case <-time.After(3 * time.Second):
					t.Fatal("no failure callback")
				}
			case "frozen":
				run.Freeze()
			case "cancelled":
				cancel()
			}
			published := false
			err, _ := run.Commit(ctx, startErr, func() error {
				if mode == "revoked" {
					return errors.New("provider generation changed")
				}
				return nil
			}, func(map[string]any) { published = true })
			if mode == "success" {
				if err != nil || !published {
					t.Fatalf("publication: %v %v", published, err)
				}
			} else if err == nil || published {
				t.Fatalf("invalid start published: %v %v", published, err)
			}
			if mode == "task" && !strings.Contains(err.Error(), "task broken: task failure") {
				t.Fatal(err)
			}
		})
	}
}

func TestFacadeDoesNotExposeLifecycleControls(t *testing.T) {
	typ := reflect.TypeOf((*gordis.Scope)(nil))
	var methods []string
	for i := 0; i < typ.NumMethod(); i++ {
		methods = append(methods, typ.Method(i).Name)
	}
	want := []string{"Acquire", "Context", "Defer", "Generation", "Go", "ID", "OnStop"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("Scope methods: %v", methods)
	}
}
