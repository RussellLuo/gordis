package build

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// A same-filesystem staging file and exclusive link refuse concurrent writers.
// Only paths created by this call are removed on a normal error.
func publish(ctx context.Context, records, candidate, output, recordPath string) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}

	staged, err := os.CreateTemp(filepath.Dir(output), ".gordis-build-")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	defer staged.Close()

	input, err := os.Open(candidate)
	if err != nil {
		return err
	}
	defer input.Close()
	if _, err = io.Copy(staged, input); err != nil {
		return err
	}
	if err = staged.Chmod(0755); err != nil {
		return err
	}
	if err = staged.Close(); err != nil {
		return err
	}

	// Reserve the record directory exclusively. Register rollback only after
	// this call owns it, so another writer's existing output is never removed.
	if err = os.Mkdir(recordPath, 0755); err != nil {
		return fmt.Errorf("refusing record output %s: %w", recordPath, err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(recordPath)
		}
	}()
	if err = copyTree(ctx, records, recordPath); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}

	// This is the commit point; no fallible operation follows the exclusive link.
	return os.Link(staged.Name(), output)
}

func copyTree(ctx context.Context, source, target string) error {
	return filepath.WalkDir(source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		relative, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, relative), 0755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("record input %s is not regular", file)
		}

		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, relative), raw, 0644)
	})
}
