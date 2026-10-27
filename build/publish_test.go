package build

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDeliveryRollbackAndConcurrentWriters(t *testing.T) {
	for _, mode := range []string{"copy failure", "existing binary", "existing records", "concurrent writers"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			candidate, records, output := filepath.Join(root, "candidate"), filepath.Join(root, "records"), filepath.Join(root, "out", "program")
			writeFixture(t, root, map[string]string{"candidate": "binary", "records/record.json": "{}", "records/logs/01.log": "logged"})
			if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
				t.Fatal(err)
			}

			switch mode {
			case "copy failure":
				if err := os.Symlink("absent", filepath.Join(records, "bad")); err != nil {
					t.Fatal(err)
				}
			case "existing binary":
				if err := os.WriteFile(output, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing records":
				writeFixture(t, output+".build", map[string]string{"sentinel": "preserve"})
			case "concurrent writers":
				var wg sync.WaitGroup
				results := make(chan error, 2)
				for range 2 {
					wg.Go(func() { results <- publish(context.Background(), records, candidate, output, output+".build") })
				}
				wg.Wait()
				close(results)
				success := 0
				for err := range results {
					if err == nil {
						success++
					}
				}
				if success != 1 {
					t.Fatal("expected one exclusive writer", success)
				}
				if raw, err := os.ReadFile(output); err != nil || string(raw) != "binary" {
					t.Fatal(string(raw), err)
				}
				if raw, err := os.ReadFile(filepath.Join(output+".build", "logs/01.log")); err != nil || string(raw) != "logged" {
					t.Fatal(string(raw), err)
				}
				return
			}

			// Failure must preserve existing outputs and remove only paths created
			// by this attempt, including its temporary staging file.
			if err := publish(context.Background(), records, candidate, output, output+".build"); err == nil {
				t.Fatal("expected failure")
			}
			if mode == "existing binary" {
				if raw, err := os.ReadFile(output); err != nil || string(raw) != "preserve" {
					t.Fatal(string(raw), err)
				}
			} else if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("partial binary", err)
			}
			if mode == "existing records" {
				if raw, err := os.ReadFile(filepath.Join(output+".build", "sentinel")); err != nil || string(raw) != "preserve" {
					t.Fatal(string(raw), err)
				}
			} else if _, err := os.Lstat(output + ".build"); !os.IsNotExist(err) {
				t.Fatal("partial records", err)
			}

			leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".gordis-build-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal(leftovers, err)
			}
		})
	}
}
