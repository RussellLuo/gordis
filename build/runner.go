package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

type runner struct {
	ctx      context.Context
	dir      string
	env      []string
	commands []CommandRecord
}

func nativeEnvironment() []string {
	overrides := map[string]string{
		// An empty GOFLAGS falls back to go env -w configuration. Whitespace
		// is nonempty to Go's config loader but parses as zero build flags.
		"GOWORK":      "off",
		"GOFLAGS":     " ",
		"GOTOOLCHAIN": "local",
		"GOOS":        runtime.GOOS,
		"GOARCH":      runtime.GOARCH,
		"CGO_ENABLED": "0",
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; !overridden {
			env = append(env, entry)
		}
	}

	keys := []string{}
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+overrides[key])
	}
	return env
}

func (r *runner) run(stage, dir string, args ...string) ([]byte, error) {
	return r.runContext(r.ctx, stage, dir, args...)
}

func (r *runner) runContext(ctx context.Context, stage, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Env = dir, r.env
	cmd.WaitDelay = time.Second

	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	code := 0
	if err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) && ctx.Err() == nil {
			code = exit.ExitCode()
		}
	}

	// Record failures too. A validation deadline is distinct from cancellation
	// of the entire build, and interrupted processes have no normal exit code.
	log := fmt.Sprintf("logs/%02d.log", len(r.commands)+1)
	r.commands = append(r.commands, CommandRecord{
		Stage:    stage,
		Args:     append([]string{}, args...),
		Log:      log,
		ExitCode: code,
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded) && r.ctx.Err() == nil,
		Canceled: r.ctx.Err() != nil,
	})
	if writeErr := writeFile(r.dir, log, append(append([]byte{}, raw...), []byte(stderr.String())...)); writeErr != nil {
		return nil, errors.Join(err, writeErr)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w\n%s%s", strings.Join(args, " "), errors.Join(err, ctx.Err()), raw, stderr.String())
	}
	return raw, nil
}
