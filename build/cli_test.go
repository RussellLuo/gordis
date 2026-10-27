package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Mixed flags, positionals and inline sources preserve input intent without
// applying application-specific version policy.
func TestArgumentsSyntaxAndIntent(t *testing.T) {
	args := []string{
		"first",
		"-with=example.com/multi/a@v1.2.3=./source",
		"--output",
		"quoted 'output",
		"--replace=example.com/dependency@v2.0.0+incompatible=example.com/fork@main",
		"second",
		"--",
		"--help",
	}
	a, err := parseArguments(args, Command{DefaultOutput: "program"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Args, args) ||
		!reflect.DeepEqual(a.Positionals, []string{"first", "second", "--help"}) ||
		!a.OutputSet ||
		a.Request.Output != "quoted 'output" {
		t.Fatal(a)
	}

	p := a.Request.Packages[0]
	if p.Path != "example.com/multi/a" ||
		p.Query != (Query{Value: "v1.2.3", Explicit: true}) ||
		p.Source.Directory != "./source" {
		t.Fatal(p)
	}

	r := a.Request.Replacements[0]
	if r.Module != "example.com/dependency" ||
		r.Version != "v2.0.0+incompatible" ||
		r.Source != (Source{Module: "example.com/fork", Query: "main"}) {
		t.Fatal(r)
	}

	a.Args[0] = "changed"
	if args[0] != "first" {
		t.Fatal("raw argv shares caller memory")
	}
}

func TestArgumentFailures(t *testing.T) {
	for _, args := range [][]string{
		{"--with"},
		{"--with="},
		{"--with", "example.com/pkg@"},
		{"--with", "example.com/pkg="},
		{"--with", "./local"},
		{"--with", "example.com/a", "--with=example.com/a"},
		{"--replace", "example.com/a"},
		{"--replace", "example.com/a=./a", "--replace", "example.com/a=./a"},
		{"--replace", "example.com/a@main=./a"},
		{"--replace", "example.com/a=example.com/fork@"},
		{"--replace", "example.com/a=example.com/fork@main=./nested"},
		{"--output", "a", "--output=b"},
		{"--unknown"},
		{"--app", "example.com/app"},
	} {
		if _, err := parseArguments(args, Command{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestAppIsOptInAndOpaque(t *testing.T) {
	command := Command{AcceptApp: true}
	a, err := parseArguments([]string{"--app=example.com/app@main=./source", "--with", "example.com/extra"}, command)
	if err != nil ||
		a.App == nil ||
		a.App.Path != "example.com/app" ||
		len(a.Request.Packages) != 1 ||
		a.Request.Packages[0].Path != "example.com/extra" {
		t.Fatal(a, err)
	}

	// App-specific policies (required app, role conflicts, positionals) belong
	// to Prepare; the common parser must not implement native conventions.
	if _, err := parseArguments(nil, command); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"--app"}, {"--app="}, {"--app", "example.com/a", "--app", "example.com/b"}} {
		if _, err := parseArguments(args, command); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestHelpDoesNotAccessSourcesOrPrepare(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing")
	command := Command{
		Name:      "app-build",
		AcceptApp: true,
		Help:      "application policy",
		Prepare: func(Arguments) (Request, Adapter, error) {
			t.Fatal("help called Prepare")
			return Request{}, Adapter{}, nil
		},
	}
	var out, diagnostic bytes.Buffer
	if code := Run(context.Background(), []string{"--with", "example.com/app=" + missing, "--help"}, &out, &diagnostic, command); code != 0 ||
		diagnostic.Len() != 0 ||
		!strings.Contains(out.String(), "application policy") ||
		!strings.Contains(out.String(), "--app") {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"--with", "--help"}, {"--", "--help"}, {"--output=--help"}, {"--app", "--help"}} {
		if helpRequested(args, true) {
			t.Fatalf("data incorrectly requested help: %v", args)
		}
	}
}

func TestBareRelativeAndRemoteSources(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("local source", 0755); err != nil {
		t.Fatal(err)
	}

	for value, want := range map[string]Source{
		"local source":     {Directory: "local source"},
		"./absent":         {Directory: "./absent"},
		"example.com/fork": {Module: "example.com/fork", Query: "latest"},
	} {
		got, err := parseSource(value)
		if err != nil || got != want {
			t.Fatal(value, got, err)
		}
	}
}

func TestRunPrepareAndExitCodes(t *testing.T) {
	command := Command{Name: "app-build", DefaultOutput: "program"}
	var diagnostic bytes.Buffer
	if code := Run(context.Background(), []string{"--unknown"}, io.Discard, &diagnostic, command); code != 2 || !strings.HasPrefix(diagnostic.String(), "app-build:") {
		t.Fatal(code, diagnostic.String())
	}

	// Usage and Prepare errors return 2; an accepted request failing Build returns 1.
	want := errors.New("application positional policy")
	command.Prepare = func(Arguments) (Request, Adapter, error) { return Request{}, Adapter{}, want }
	if code := Run(context.Background(), nil, io.Discard, io.Discard, command); code != 2 {
		t.Fatal(code)
	}

	calls := 0
	command.Prepare = func(a Arguments) (Request, Adapter, error) {
		calls++
		if a.Request.Dir == "" || a.Request.Output != platformOutput("program") || a.OutputSet {
			return Request{}, Adapter{}, fmt.Errorf("incorrect defaults: %+v", a)
		}
		return a.Request, Adapter{}, nil // Build input error, not a usage error.
	}
	if code := Run(context.Background(), nil, io.Discard, io.Discard, command); code != 1 || calls != 1 {
		t.Fatal(code, calls)
	}
}
