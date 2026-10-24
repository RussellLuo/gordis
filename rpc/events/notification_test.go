package events

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
)

// A Notice handler may observe the payload before it returns. The nested
// Child Publish and original Host Emit must still wait and propagate its error.
func TestBridgeNestedNotificationCompletion(t *testing.T) {
	for _, name := range []string{"success", "handler-error"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, processSetup{"a", testBridge(t, true), []string{"GORDIS_EV02_EXTRA=1"}})
			h.process("a")
			received := make(chan string, 1)
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			t.Cleanup(release)
			var want error
			if name == "handler-error" {
				want = gordis.ErrStale
			}
			_, err := core.Bind(h.scope).On(other, func(ctx context.Context, msg string) error {
				received <- msg
				select {
				case <-gate:
					return want
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- core.Bind(h.scope).Emit(h.ctx, notice, "request-notice")
			}()
			select {
			case msg := <-received:
				if msg != "reply" {
					t.Fatalf("Notice: got %q, want reply", msg)
				}
			case <-h.ctx.Done():
				t.Fatal("Notice not received", h.ctx.Err())
			}
			select {
			case err := <-done:
				t.Fatalf("Emit returned before the Notice handler finished: %v", err)
			default:
			}
			release()
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("Emit: got %v, want %v", err, want)
				}
			case <-h.ctx.Done():
				t.Fatal("Emit did not finish", h.ctx.Err())
			}
		})
	}
}
