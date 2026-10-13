package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/internal/lifecycle"
)

func TestCommitRejectsFailedIncompleteOrRevokedStart(t *testing.T) {
	key := gordis.NewKey[int]("value")
	for _, mode := range []string{"missing", "factory", "task", "frozen", "cancelled", "revoked", "success"} {
		t.Run(mode, func(t *testing.T) {
			failure := make(chan error, 1)
			run := lifecycle.New("plugin", 8, nil, []gordis.ServiceSpec{key.Spec()}, nil, func(err error) { failure <- err })
			scope := &gordis.Scope{Scope: run.Scope()}
			defer func() { run.Freeze(); run.Quiesce(); _ = run.Dispose() }()
			if mode != "missing" {
				if err := scope.Provide(key, 42); err != nil {
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

func TestReadyAdmissionAndAfterReady(t *testing.T) {
	run := lifecycle.New("plugin", 1, nil, nil, nil, nil)
	scope := &gordis.Scope{Scope: run.Scope()}
	if _, err := scope.AcquireReady(); !errors.Is(err, gordis.ErrNotReady) {
		t.Fatalf("AcquireReady before commit: %v", err)
	}
	started := make(chan struct{})
	releaseTask := make(chan struct{})
	if err := scope.AfterReady("post-commit", func(context.Context) error {
		close(started)
		<-releaseTask
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
		t.Fatal("AfterReady task started before commit")
	default:
	}
	if err, _ := run.Commit(context.Background(), nil, nil, func(map[string]any) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("AfterReady task did not start")
	}
	release, err := scope.AcquireReady()
	if err != nil {
		t.Fatal(err)
	}
	release()
	run.Freeze()
	if _, err := scope.AcquireReady(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("AcquireReady after freeze: %v", err)
	}
	run.Quiesce()
	close(releaseTask)
	if err := run.Dispose(); err != nil {
		t.Fatal(err)
	}
}
