package gordis_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
)

func applyAfterSettled(t *testing.T, changes *gordis.ChangeSet) (*gordis.Operation, error) {
	t.Helper()
	ctx := deadline(t)
	for {
		operation, err := changes.Apply(ctx)
		if !errors.Is(err, gordis.ErrBusy) {
			return operation, err
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

type versionPlugin struct {
	Version   string `json:"Version"`
	rejectOld *atomic.Bool
}

func (p *versionPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:  "d",
		New: func() gordis.Plugin { return &versionPlugin{rejectOld: p.rejectOld} },
	}
}

func (p *versionPlugin) Activate(context.Context, *gordis.Scope) error {
	if p.Version == "bad" {
		return errors.New("upgrade rejected")
	}
	if p.rejectOld.Load() {
		return errors.New("restore rejected")
	}
	return nil
}

func TestChangeSetAtomicMountMountedOrderAndExactReadiness(t *testing.T) {
	values := make(chan string, 4)
	h, err := gordis.NewHost([]gordis.Plugin{new(p2Provider), &p2Consumer{values: values}})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	changes := h.Changes()
	changes.Mount(h.Env(), gordis.InstanceSpec{ID: "consumer", Plugin: "p2-consumer"})
	changes.Mount(h.Env(), gordis.InstanceSpec{
		ID: "provider", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"one"}`),
	})
	operation, err := changes.Apply(deadline(t))
	must(t, err)
	if got := operation.Snapshot().Affected; len(got) != 2 || got[0] != "consumer" || got[1] != "provider" {
		t.Fatalf("affected = %v", got)
	}
	mounted := operation.Mounted()
	if len(mounted) != 2 || mounted[0].ID() != "consumer" || mounted[1].ID() != "provider" {
		t.Fatalf("mounted order = %#v", mounted)
	}
	must(t, operation.WaitReady(deadline(t)))
	if got := recv(t, values); got != "one" {
		t.Fatalf("value = %q", got)
	}
	if snapshot := operation.Snapshot(); !snapshot.Committed || snapshot.Revision == 0 {
		t.Fatalf("operation snapshot = %+v", snapshot)
	}
}

func TestChangeSetProviderRemovalMakesConsumerPendingAndRestores(t *testing.T) {
	values := make(chan string, 4)
	h, err := gordis.NewHost([]gordis.Plugin{new(p2Provider), &p2Consumer{values: values}})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	consumer, err := h.Env().Mount(context.Background(), gordis.InstanceSpec{
		ID: "consumer", Plugin: "p2-consumer",
	})
	must(t, err)
	provider, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "provider", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"one"}`),
	})
	must(t, err)
	must(t, consumer.WaitReady(deadline(t)))
	if got := recv(t, values); got != "one" {
		t.Fatalf("value = %q", got)
	}

	changes := h.Changes()
	changes.Unmount(provider)
	removed, err := changes.Apply(deadline(t))
	must(t, err)
	must(t, removed.Wait(deadline(t)))
	if snapshot := find(h, "consumer"); snapshot.Phase != gordis.PhasePending {
		t.Fatalf("consumer after removal = %+v", snapshot)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := removed.WaitReady(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending operation readiness = %v", err)
	}

	restored, err := removed.Restore(deadline(t))
	must(t, err)
	must(t, restored.WaitReady(deadline(t)))
	if mounted := restored.Mounted(); len(mounted) != 1 || mounted[0].ID() != "provider" {
		t.Fatalf("restored mounts = %#v", mounted)
	}
	if got := recv(t, values); got != "one" {
		t.Fatalf("restored value = %q", got)
	}
	if err := provider.WaitReady(context.Background()); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("removed handle = %v", err)
	}
}

func TestChangeSetAtomicReplacementAndRestoreUsesNewLogicalHandles(t *testing.T) {
	values := make(chan string, 4)
	h, err := gordis.NewHost([]gordis.Plugin{new(p2Provider), &p2Consumer{values: values}})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	consumer, err := h.Env().Mount(context.Background(), gordis.InstanceSpec{
		ID: "consumer", Plugin: "p2-consumer",
	})
	must(t, err)
	oldProvider, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "provider", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"old"}`),
	})
	must(t, err)
	must(t, consumer.WaitReady(deadline(t)))
	if got := recv(t, values); got != "old" {
		t.Fatalf("old value = %q", got)
	}

	replace := h.Changes()
	replace.Unmount(oldProvider)
	replace.Mount(h.Env(), gordis.InstanceSpec{
		ID: "provider", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"new"}`),
	})
	replacement, err := replace.Apply(deadline(t))
	must(t, err)
	must(t, replacement.WaitReady(deadline(t)))
	newProvider := replacement.Mounted()[0]
	if got := recv(t, values); got != "new" {
		t.Fatalf("replacement value = %q", got)
	}
	if err := oldProvider.WaitReady(context.Background()); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("old handle = %v", err)
	}

	restored, err := replacement.Restore(deadline(t))
	must(t, err)
	must(t, restored.WaitReady(deadline(t)))
	restoredProvider := restored.Mounted()[0]
	if got := recv(t, values); got != "old" {
		t.Fatalf("restored value = %q", got)
	}
	if restoredProvider == newProvider {
		t.Fatal("restore reused replacement handle")
	}
	if err := newProvider.WaitReady(context.Background()); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("replacement handle after restore = %v", err)
	}
	if staleRestore, err := replacement.Restore(deadline(t)); staleRestore != nil || !errors.Is(err, gordis.ErrStale) {
		t.Fatalf("stale restore=%v err=%v", staleRestore, err)
	}
}

func TestLoaderStyleEntryOverlayAndDisableUseOnlyPublicControlPlane(t *testing.T) {
	values := make(chan string, 4)
	h, err := gordis.NewHost([]gordis.Plugin{new(p2Provider), &p2Consumer{values: values}})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	tenant := h.Env().Isolate(p2ValueKey, "tenant-a")

	entries := h.Changes()
	entries.Mount(tenant, gordis.InstanceSpec{ID: "reader", Plugin: "p2-consumer"})
	entries.Mount(tenant, gordis.InstanceSpec{
		ID: "store", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"base"}`),
	})
	loaded, err := entries.Apply(deadline(t))
	must(t, err)
	must(t, loaded.WaitReady(deadline(t)))
	reader, store := loaded.Mounted()[0], loaded.Mounted()[1]
	if got := recv(t, values); got != "base" {
		t.Fatalf("base value = %q", got)
	}

	overlay := h.Changes()
	overlay.Update(store, json.RawMessage(`{"value":"overlay"}`))
	updated, err := applyAfterSettled(t, overlay)
	must(t, err)
	must(t, updated.WaitReady(deadline(t)))
	if got := recv(t, values); got != "overlay" {
		t.Fatalf("overlay value = %q", got)
	}

	disable := h.Changes()
	disable.Unmount(reader)
	disable.Unmount(store)
	disabled, err := applyAfterSettled(t, disable)
	must(t, err)
	must(t, disabled.Wait(deadline(t)))
	if snapshots := h.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("disabled entries remain: %+v", snapshots)
	}
}

func TestAcceptedChangeSetTimeoutRemainsObservable(t *testing.T) {
	scopes := make(chan *gordis.Scope, 2)
	plugin := definition("leased", func(_ context.Context, scope *gordis.Scope) error {
		scopes <- scope
		return nil
	})
	h, err := gordis.NewHost([]gordis.Plugin{plugin})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	instance, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "leased", Plugin: "leased"})
	must(t, err)
	first := recv(t, scopes)
	release, err := first.Acquire()
	must(t, err)

	changes := h.Changes()
	changes.Restart(instance)
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	operation, err := changes.Apply(short)
	if operation == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation=%v err=%v", operation, err)
	}
	if snapshot := operation.Snapshot(); snapshot.Committed || snapshot.Phase != "draining" {
		t.Fatalf("timed out operation = %+v", snapshot)
	}
	release()
	must(t, operation.Wait(deadline(t)))
	must(t, operation.WaitReady(deadline(t)))
	_ = recv(t, scopes)
}

func TestOperationSeparatesActivationAndRestoreFailures(t *testing.T) {
	var rejectOld atomic.Bool
	plugin := &versionPlugin{rejectOld: &rejectOld}
	h, err := gordis.NewHost([]gordis.Plugin{plugin})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	instance, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "versioned", Plugin: "d", Config: json.RawMessage(`{"Version":"old"}`),
	})
	must(t, err)

	upgrade := h.Changes()
	upgrade.Update(instance, json.RawMessage(`{"Version":"bad"}`))
	failedUpgrade, err := applyAfterSettled(t, upgrade)
	must(t, err)
	if err := failedUpgrade.WaitReady(deadline(t)); err == nil || !strings.Contains(err.Error(), "upgrade rejected") {
		t.Fatalf("upgrade readiness = %v", err)
	}
	if err := failedUpgrade.Wait(deadline(t)); err == nil || !strings.Contains(err.Error(), "upgrade rejected") {
		t.Fatalf("upgrade convergence = %v", err)
	}

	restored, err := failedUpgrade.Restore(deadline(t))
	must(t, err)
	must(t, restored.WaitReady(deadline(t)))
	if snapshot := restored.Snapshot(); snapshot.Restores != failedUpgrade.Snapshot().ID {
		t.Fatalf("restore relation = %+v", snapshot)
	}

	upgrade = h.Changes()
	upgrade.Update(instance, json.RawMessage(`{"Version":"bad"}`))
	failedUpgrade, err = applyAfterSettled(t, upgrade)
	must(t, err)
	if err := failedUpgrade.Wait(deadline(t)); err == nil {
		t.Fatal("second bad upgrade succeeded")
	}
	rejectOld.Store(true)
	failedRestore, err := failedUpgrade.Restore(deadline(t))
	must(t, err)
	if err := failedRestore.WaitReady(deadline(t)); err == nil || !strings.Contains(err.Error(), "restore rejected") {
		t.Fatalf("restore readiness = %v", err)
	}
	if original := failedUpgrade.Snapshot(); !strings.Contains(original.Error, "upgrade rejected") {
		t.Fatalf("original diagnostics were overwritten: %+v", original)
	}
	if recovery := failedRestore.Snapshot(); !strings.Contains(recovery.Error, "restore rejected") {
		t.Fatalf("restore diagnostics = %+v", recovery)
	}
}

func TestOperationWaitReadyObservesFailureBeyondAnotherPendingTarget(t *testing.T) {
	fail := make(chan error, 1)
	runtime := definition("runtime", func(_ context.Context, scope *gordis.Scope) error {
		return scope.Go("fail", func(ctx context.Context) error {
			select {
			case err := <-fail:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	})
	h, err := gordis.NewHost([]gordis.Plugin{&p2Consumer{values: make(chan string, 1)}, runtime})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	changes := h.Changes()
	changes.Mount(h.Env(), gordis.InstanceSpec{ID: "a-pending", Plugin: "p2-consumer"})
	changes.Mount(h.Env(), gordis.InstanceSpec{ID: "z-runtime", Plugin: "runtime"})
	operation, err := changes.Apply(deadline(t))
	must(t, err)
	must(t, operation.Wait(deadline(t)))

	runtimeFailure := errors.New("runtime target failed")
	fail <- runtimeFailure
	if err := operation.WaitReady(deadline(t)); !errors.Is(err, runtimeFailure) {
		t.Fatalf("operation readiness = %v", err)
	}
}

func TestChangeSetRejectsConflicts(t *testing.T) {
	h, err := gordis.NewHost([]gordis.Plugin{definition("plain", nil)})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	instance, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "one", Plugin: "plain"})
	must(t, err)

	conflict := h.Changes()
	conflict.Update(instance, json.RawMessage(`{}`))
	conflict.Unmount(instance)
	if _, err := applyAfterSettled(t, conflict); err == nil {
		t.Fatal("conflicting instance changes were accepted")
	}
}

func TestChangeSetRejectsOwnerGenerationThatFailedBeforeApply(t *testing.T) {
	fail := make(chan error, 1)
	parent := definition("parent", func(_ context.Context, scope *gordis.Scope) error {
		return scope.Go("fail", func(ctx context.Context) error {
			select {
			case err := <-fail:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	})
	h, err := gordis.NewHost([]gordis.Plugin{parent, definition("child", nil)})
	must(t, err)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	owner, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "owner", Plugin: "parent"})
	must(t, err)

	changes := h.Changes()
	changes.Mount(owner.Env(), gordis.InstanceSpec{ID: "child", Plugin: "child"})
	fail <- errors.New("owner failed")
	awaitState(t, func() bool {
		snapshot := find(h, "owner")
		return snapshot.Phase == gordis.PhaseStopped && snapshot.Failure != ""
	})
	if operation, err := applyAfterSettled(t, changes); operation != nil || !errors.Is(err, gordis.ErrStale) {
		t.Fatalf("owner-stale operation=%v err=%v", operation, err)
	}
}
