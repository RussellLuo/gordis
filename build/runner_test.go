package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeEnvironmentClearsPersistedGoFlags(t *testing.T) {
	root := t.TempDir()
	proxy := "file://" + filepath.ToSlash(root)
	writeFixture(t, root, map[string]string{"go-env": "GOFLAGS=-tags=persisted\nGOPROXY=" + proxy + "\n"})
	t.Setenv("GOENV", filepath.Join(root, "go-env"))
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPROXY", "")
	r := &runner{ctx: context.Background(), dir: root, env: nativeEnvironment()}
	raw, err := r.run("input", root, "go", "env", "GOFLAGS", "GOPROXY")
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != "" || lines[1] != proxy {
		t.Fatalf("expected no flags and preserved proxy configuration, got %q", raw)
	}

	// A file enabled only by persisted flags would fail compilation if the
	// builder accidentally inherited those flags.
	request := localRequest(t)
	writeFixture(t, request.Dir, map[string]string{
		"source 'tree/a/hidden.go": "//go:build persisted\n\npackage a\nconst Hidden = undefinedSymbol\n",
	})
	result, err := Build(context.Background(), request, valueAdapter())
	if err != nil {
		t.Fatal(err)
	}
	if flags := readRecord(t, result).Environment["GOFLAGS"]; flags != "" {
		t.Fatalf("recorded effective flags: %q", flags)
	}
}

// The runner executes this test binary as a child to produce a known exit
// status or wait for cancellation, without relying on a platform shell.
func TestCommandHelper(t *testing.T) {
	if os.Getenv("GORDIS_BUILD_COMMAND_HELPER") != "1" {
		return
	}
	if os.Args[len(os.Args)-1] == "wait" {
		time.Sleep(time.Hour)
	}
	fmt.Fprintln(os.Stderr, "candidate diagnostic")
	os.Exit(7)
}

func TestCommandExitTimeoutAndOuterCancellation(t *testing.T) {
	for _, mode := range []string{"exit", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			outer, stop := context.WithCancel(context.Background())
			defer stop()
			r := &runner{ctx: outer, dir: t.TempDir(), env: append(nativeEnvironment(), "GORDIS_BUILD_COMMAND_HELPER=1")}
			ctx := outer
			command := "wait"
			if mode == "exit" {
				command = "exit"
			}
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(outer, 100*time.Millisecond)
				defer cancel()
			}
			if mode == "cancel" {
				timer := time.AfterFunc(100*time.Millisecond, stop)
				defer timer.Stop()
			}

			_, err := r.runContext(ctx, "validate", r.dir, os.Args[0], "-test.run=^TestCommandHelper$", "--", command)
			if err == nil || len(r.commands) != 1 {
				t.Fatal(err, r.commands)
			}

			record := r.commands[0]
			switch mode {
			case "exit":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || record.ExitCode != 7 || record.TimedOut || record.Canceled {
					t.Fatal(err, record)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) || record.ExitCode != -1 || !record.TimedOut || record.Canceled {
					t.Fatal(err, record)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) || record.ExitCode != -1 || record.TimedOut || !record.Canceled {
					t.Fatal(err, record)
				}
			}
		})
	}
}
