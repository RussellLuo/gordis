package gordis_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/RussellLuo/gordis"
)

type p3Service interface{ Value() string }
type p3ServiceValue string

func (v p3ServiceValue) Value() string { return string(v) }

var p3Key = gordis.NewKey[p3Service]("p3.service")

type p3Provider struct{}

func (*p3Provider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "p3-provider", Provides: []gordis.ServiceSpec{p3Key.Spec()},
		New: func() gordis.Plugin { return new(p3Provider) },
	}
}

func (*p3Provider) Activate(_ context.Context, scope *gordis.Scope) error {
	return gordis.Provide[p3Service](scope, p3Key, p3ServiceValue("child"))
}

type p3Consumer struct{ values chan<- string }

func (p *p3Consumer) Spec() gordis.PluginSpec {
	values := p.values
	return gordis.PluginSpec{
		ID: "p3-consumer", Requires: []gordis.ServiceSpec{p3Key.Spec()},
		New: func() gordis.Plugin { return &p3Consumer{values: values} },
	}
}

func (p *p3Consumer) Activate(_ context.Context, scope *gordis.Scope) error {
	value, err := gordis.Get(scope, p3Key)
	if err == nil && p.values != nil {
		p.values <- value.Value()
	}
	return err
}

type p3Children struct {
	provider *gordis.Instance
	consumer *gordis.Instance
}

type p3Composer struct {
	children chan<- p3Children
	values   chan<- string
}

func (p *p3Composer) Spec() gordis.PluginSpec {
	children, values := p.children, p.values
	return gordis.PluginSpec{
		ID:  "p3-composer",
		New: func() gordis.Plugin { return &p3Composer{children: children, values: values} },
	}
}

func (p *p3Composer) Activate(ctx context.Context, scope *gordis.Scope) error {
	env := scope.Env().Isolate(p3Key, "p3-private")
	consumer, err := env.Mount(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "p3-consumer"})
	if err != nil {
		return err
	}
	provider, err := env.Mount(ctx, gordis.InstanceSpec{ID: "provider", Plugin: "p3-provider"})
	if err != nil {
		return err
	}
	if p.children != nil {
		p.children <- p3Children{provider: provider, consumer: consumer}
	}
	return nil
}

func TestNestedMountSiblingBindingAndGenerationOwnership(t *testing.T) {
	children := make(chan p3Children, 2)
	values := make(chan string, 2)
	h, err := gordis.NewHost([]gordis.Plugin{
		&p3Composer{children: children, values: values}, new(p3Provider), &p3Consumer{values: values},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	parent, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "parent", Plugin: "p3-composer"})
	if err != nil {
		t.Fatal(err)
	}
	first := recv(t, children)
	if first.provider.ID() != "provider" || first.consumer.ID() != "consumer" {
		t.Fatalf("child local IDs: %q %q", first.provider.ID(), first.consumer.ID())
	}
	if got := recv(t, values); got != "child" {
		t.Fatalf("consumer value = %q", got)
	}
	if snapshot := find(h, "parent"); snapshot.Phase != gordis.PhaseReady {
		t.Fatalf("parent is not ready: %+v", snapshot)
	}
	providerSnapshot := find(h, "parent/provider")
	consumerSnapshot := find(h, "parent/consumer")
	if providerSnapshot.Parent != "parent" || consumerSnapshot.Parent != "parent" ||
		providerSnapshot.View[p3Key.Name()] != "p3-private" ||
		consumerSnapshot.Bindings[0].Provider != "parent/provider" {
		t.Fatalf("nested placement/binding: provider=%+v consumer=%+v", providerSnapshot, consumerSnapshot)
	}

	oldParentEnv := parent.Env()
	oldEnv := first.provider.Env()
	if err := parent.Restart(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if err := parent.WaitReady(deadline(t)); err != nil {
		t.Fatal(err)
	}
	second := recv(t, children)
	if got := recv(t, values); got != "child" {
		t.Fatalf("restarted consumer value = %q", got)
	}
	if err := first.provider.WaitReady(context.Background()); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("old child handle error = %v", err)
	}
	if _, err := oldParentEnv.Mount(context.Background(), gordis.InstanceSpec{ID: "late", Plugin: "p3-provider"}); !errors.Is(err, gordis.ErrStale) {
		t.Fatalf("old parent Env error = %v", err)
	}
	if _, err := oldEnv.Mount(context.Background(), gordis.InstanceSpec{ID: "late", Plugin: "p3-provider"}); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("old child Env error = %v", err)
	}
	if second.provider == first.provider || second.provider.ID() != "provider" {
		t.Fatal("replacement generation reused an old child handle")
	}
}

func TestRestoreParentRequiresLoaderToReconcileOwnedChildren(t *testing.T) {
	parentPlugin := definition("p3-loader-parent", nil)
	childPlugin := definition("p3-loader-child", nil)
	host, err := gordis.NewHost([]gordis.Plugin{parentPlugin, childPlugin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })

	parent, err := host.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "parent", Plugin: "p3-loader-parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "child", Plugin: "p3-loader-child",
	})
	if err != nil {
		t.Fatal(err)
	}

	changes := host.Changes()
	changes.Unmount(parent)
	removed, err := applyAfterSettled(t, changes)
	if err != nil {
		t.Fatal(err)
	}
	if err := removed.Wait(deadline(t)); err != nil {
		t.Fatal(err)
	}
	restored, err := removed.Restore(deadline(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.WaitReady(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if snapshots := host.Snapshot(); len(snapshots) != 1 || snapshots[0].QualifiedID != "parent" {
		t.Fatalf("Restore revived an old-generation child: %+v", snapshots)
	}
	if err := child.WaitReady(context.Background()); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("old child handle = %v", err)
	}

	mounted := restored.Mounted()
	if len(mounted) != 1 {
		t.Fatalf("restored mounts = %#v", mounted)
	}
	restoredParent := mounted[0]
	if _, err := restoredParent.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "child", Plugin: "p3-loader-child",
	}); err != nil {
		t.Fatalf("Loader reconcile child: %v", err)
	}
}

type p3CycleParent struct{}

func (*p3CycleParent) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "p3-cycle-parent", Provides: []gordis.ServiceSpec{p3Key.Spec()},
		New: func() gordis.Plugin { return new(p3CycleParent) },
	}
}

func (*p3CycleParent) Activate(ctx context.Context, scope *gordis.Scope) error {
	_, err := scope.Env().MountReady(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "p3-consumer"})
	if err != nil {
		return err
	}
	return gordis.Provide[p3Service](scope, p3Key, p3ServiceValue("parent"))
}

func TestNestedMountDetectsActivationWaitCycleAndRollsBackSubtree(t *testing.T) {
	h, err := gordis.NewHost([]gordis.Plugin{new(p3CycleParent), &p3Consumer{}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "parent", Plugin: "p3-cycle-parent"})
	if instance == nil || !errors.Is(err, gordis.ErrActivationCycle) {
		t.Fatalf("instance=%v err=%v", instance, err)
	}
	awaitState(t, func() bool {
		for _, snapshot := range h.Snapshot() {
			if snapshot.QualifiedID == "parent/consumer" {
				return false
			}
		}
		return true
	})
	for _, snapshot := range h.Snapshot() {
		if snapshot.QualifiedID == "parent/consumer" {
			t.Fatalf("provisional child survived parent failure: %+v", snapshot)
		}
	}
	if snapshot := find(h, "parent"); snapshot.Phase != gordis.PhaseStopped || snapshot.Failure == "" {
		t.Fatalf("parent failure diagnostics: %+v", snapshot)
	}
}

type p3PublishingParent struct{}

func (*p3PublishingParent) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "p3-publishing-parent", Provides: []gordis.ServiceSpec{p3Key.Spec()},
		New: func() gordis.Plugin { return new(p3PublishingParent) },
	}
}

func (*p3PublishingParent) Activate(ctx context.Context, scope *gordis.Scope) error {
	if _, err := scope.Env().Mount(ctx, gordis.InstanceSpec{ID: "consumer", Plugin: "p3-consumer"}); err != nil {
		return err
	}
	return gordis.Provide[p3Service](scope, p3Key, p3ServiceValue("ancestor"))
}

func TestNonBlockingNestedMountWaitsForAncestorPublication(t *testing.T) {
	values := make(chan string, 1)
	h, err := gordis.NewHost([]gordis.Plugin{new(p3PublishingParent), &p3Consumer{values: values}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	parent, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "parent", Plugin: "p3-publishing-parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if value := recv(t, values); value != "ancestor" {
		t.Fatalf("ancestor binding value = %q", value)
	}
	if snapshot := find(h, "parent"); snapshot.Phase != gordis.PhaseReady || parent == nil {
		t.Fatalf("parent readiness: %+v", snapshot)
	}
}

type p3FailingChild struct {
	failure error
}

func (p *p3FailingChild) Spec() gordis.PluginSpec {
	failure := p.failure
	return gordis.PluginSpec{
		ID:  "p3-failing-child",
		New: func() gordis.Plugin { return &p3FailingChild{failure: failure} },
	}
}

func (p *p3FailingChild) Activate(context.Context, *gordis.Scope) error { return p.failure }

type p3FailureParent struct {
	child chan<- *gordis.Instance
}

func (p *p3FailureParent) Spec() gordis.PluginSpec {
	child := p.child
	return gordis.PluginSpec{
		ID:  "p3-failure-parent",
		New: func() gordis.Plugin { return &p3FailureParent{child: child} },
	}
}

func (p *p3FailureParent) Activate(ctx context.Context, scope *gordis.Scope) error {
	child, err := scope.Env().Mount(ctx, gordis.InstanceSpec{
		ID: "child", Plugin: "p3-failing-child",
	})
	if err == nil && p.child != nil {
		p.child <- child
	}
	return err
}

func TestNestedFailureDoesNotAffectParentReadiness(t *testing.T) {
	failure := errors.New("nested child failed")
	children := make(chan *gordis.Instance, 1)
	parentPlugin := &p3FailureParent{child: children}
	h, err := gordis.NewHost([]gordis.Plugin{parentPlugin, &p3FailingChild{failure: failure}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	parent, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "parent", Plugin: parentPlugin.Spec().ID,
	})
	if err != nil || parent == nil {
		t.Fatalf("parent=%v err=%v", parent, err)
	}
	child := recv(t, children)
	if err := child.WaitReady(deadline(t)); !errors.Is(err, failure) {
		t.Fatalf("child readiness = %v", err)
	}
	if err := parent.WaitReady(deadline(t)); err != nil {
		t.Fatalf("parent readiness = %v", err)
	}
	awaitState(t, func() bool {
		for _, snapshot := range h.Snapshot() {
			if snapshot.QualifiedID == "parent/child" {
				return snapshot.Phase == gordis.PhaseStopped && snapshot.Failure != ""
			}
		}
		return false
	})
	if parentSnapshot := find(h, "parent"); parentSnapshot.Phase != gordis.PhaseReady {
		t.Fatalf("parent snapshot: %+v", parentSnapshot)
	}
	childSnapshot := find(h, "parent/child")
	if childSnapshot.Phase != gordis.PhaseStopped || childSnapshot.Failure == "" {
		t.Fatalf("child snapshot: %+v", childSnapshot)
	}
}

type p3PendingParent struct {
	child chan<- *gordis.Instance
}

func (p *p3PendingParent) Spec() gordis.PluginSpec {
	child := p.child
	return gordis.PluginSpec{
		ID:  "p3-pending-parent",
		New: func() gordis.Plugin { return &p3PendingParent{child: child} },
	}
}

func (p *p3PendingParent) Activate(ctx context.Context, scope *gordis.Scope) error {
	child, err := scope.Env().Mount(ctx, gordis.InstanceSpec{ID: "child", Plugin: "p3-consumer"})
	if err == nil && p.child != nil {
		p.child <- child
	}
	return err
}

func TestOperationWaitReadyTargetsParentNotPendingChild(t *testing.T) {
	children := make(chan *gordis.Instance, 1)
	values := make(chan string, 2)
	parentPlugin := &p3PendingParent{child: children}
	h, err := gordis.NewHost([]gordis.Plugin{
		parentPlugin, &p3Consumer{values: values}, new(p3Provider),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	changes := h.Changes()
	changes.Mount(h.Env(), gordis.InstanceSpec{ID: "parent", Plugin: parentPlugin.Spec().ID})
	operation, err := changes.Apply(deadline(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.WaitReady(deadline(t)); err != nil {
		t.Fatalf("operation readiness = %v", err)
	}
	mounted := operation.Mounted()
	if len(mounted) != 1 {
		t.Fatalf("mounted targets = %#v", mounted)
	}
	parent := mounted[0]
	child := recv(t, children)
	awaitState(t, func() bool {
		return find(h, "parent/child").Phase == gordis.PhasePending
	})
	if err := parent.WaitReady(deadline(t)); err != nil {
		t.Fatalf("parent readiness = %v", err)
	}
	if snapshot := find(h, "parent"); snapshot.Phase != gordis.PhaseReady {
		t.Fatalf("parent snapshot: %+v", snapshot)
	}
	if snapshot := find(h, "parent/child"); snapshot.Phase != gordis.PhasePending {
		t.Fatalf("child snapshot: %+v", snapshot)
	}

	provider, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "provider", Plugin: "p3-provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := child.WaitReady(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, values); got != "child" {
		t.Fatalf("first child value = %q", got)
	}
	before := find(h, "parent/child").Generation
	if err := provider.Restart(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if err := child.WaitReady(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, values); got != "child" {
		t.Fatalf("recovered child value = %q", got)
	}
	if after := find(h, "parent/child").Generation; after <= before {
		t.Fatalf("child generation did not advance: %d -> %d", before, after)
	}
}

func TestSnapshotJSONOmitsOwnershipReadinessFields(t *testing.T) {
	h, err := gordis.NewHost([]gordis.Plugin{definition("plain", nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	if _, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "plain", Plugin: "plain"}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(find(h, "plain"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"option" + "al", "group" + "Ready"} {
		if _, ok := fields[name]; ok {
			t.Fatalf("snapshot JSON contains removed field %q: %s", name, payload)
		}
	}
}

type p3RollbackParent struct {
	child   chan<- *gordis.Instance
	cleaned chan<- string
	failure error
}

func (p *p3RollbackParent) Spec() gordis.PluginSpec {
	child, cleaned, failure := p.child, p.cleaned, p.failure
	return gordis.PluginSpec{
		ID: "p3-rollback-parent",
		New: func() gordis.Plugin {
			return &p3RollbackParent{child: child, cleaned: cleaned, failure: failure}
		},
	}
}

func (p *p3RollbackParent) Activate(ctx context.Context, scope *gordis.Scope) error {
	child, err := scope.Env().MountReady(ctx, gordis.InstanceSpec{ID: "child", Plugin: "p3-cleanup-child"})
	if err != nil {
		return err
	}
	p.child <- child
	return p.failure
}

type p3CleanupChild struct{ cleaned chan<- string }

func (p *p3CleanupChild) Spec() gordis.PluginSpec {
	cleaned := p.cleaned
	return gordis.PluginSpec{
		ID:  "p3-cleanup-child",
		New: func() gordis.Plugin { return &p3CleanupChild{cleaned: cleaned} },
	}
}

func (p *p3CleanupChild) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Defer("p3 child", func(context.Context) error {
		p.cleaned <- scope.ID()
		return nil
	})
}

func TestParentActivationFailureCleansAndRemovesProvisionalChildren(t *testing.T) {
	failure := errors.New("parent activation failed")
	children := make(chan *gordis.Instance, 1)
	cleaned := make(chan string, 1)
	h, err := gordis.NewHost([]gordis.Plugin{
		&p3RollbackParent{child: children, cleaned: cleaned, failure: failure},
		&p3CleanupChild{cleaned: cleaned},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "parent", Plugin: "p3-rollback-parent"})
	if parent == nil || !errors.Is(err, failure) {
		t.Fatalf("parent=%v err=%v", parent, err)
	}
	child := recv(t, children)
	if got := recv(t, cleaned); got != "parent/child" {
		t.Fatalf("cleaned child = %q", got)
	}
	if err := child.WaitReady(context.Background()); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("provisional child handle = %v", err)
	}
	if len(h.Snapshot()) != 1 {
		t.Fatalf("provisional subtree survived: %+v", h.Snapshot())
	}
}

// Keep the race detector exercising concurrent Snapshot calls while nested
// callbacks commit and converge.
func TestNestedMountSnapshotReentry(t *testing.T) {
	children := make(chan p3Children, 1)
	values := make(chan string, 1)
	h, err := gordis.NewHost([]gordis.Plugin{
		&p3Composer{children: children, values: values}, new(p3Provider), &p3Consumer{values: values},
	})
	if err != nil {
		t.Fatal(err)
	}
	var snapshots sync.WaitGroup
	snapshots.Add(1)
	done := make(chan struct{})
	go func() {
		defer snapshots.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = h.Snapshot()
			}
		}
	}()
	_, err = h.Env().MountReady(deadline(t), gordis.InstanceSpec{ID: "parent", Plugin: "p3-composer"})
	close(done)
	snapshots.Wait()
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Shutdown(context.Background())
}

type p3RecursiveState struct {
	mu      sync.Mutex
	cleaned []string
}

type p3Recursive struct {
	Depth int `json:"depth"`
	state *p3RecursiveState
}

func (p *p3Recursive) Spec() gordis.PluginSpec {
	state := p.state
	return gordis.PluginSpec{
		ID:  "p3-recursive",
		New: func() gordis.Plugin { return &p3Recursive{state: state} },
	}
}

func (p *p3Recursive) Activate(ctx context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("recursive", func(context.Context) error {
		p.state.mu.Lock()
		p.state.cleaned = append(p.state.cleaned, scope.ID())
		p.state.mu.Unlock()
		return nil
	}); err != nil {
		return err
	}
	if p.Depth == 0 {
		return nil
	}
	config, err := json.Marshal(p3Recursive{Depth: p.Depth - 1})
	if err != nil {
		return err
	}
	_, err = scope.Env().Mount(ctx, gordis.InstanceSpec{
		ID: "next", Plugin: "p3-recursive", Config: config,
	})
	return err
}

func TestRecursiveNestedMountAndOwnedCleanupOrder(t *testing.T) {
	state := new(p3RecursiveState)
	h, err := gordis.NewHost([]gordis.Plugin{&p3Recursive{state: state}})
	if err != nil {
		t.Fatal(err)
	}
	config := json.RawMessage(`{"depth":2}`)
	root, err := h.Env().MountReady(deadline(t), gordis.InstanceSpec{
		ID: "root", Plugin: "p3-recursive", Config: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitState(t, func() bool {
		for _, snapshot := range h.Snapshot() {
			if snapshot.QualifiedID == "root/next/next" {
				return snapshot.Phase == gordis.PhaseReady
			}
		}
		return false
	})
	if snapshot := find(h, "root/next/next"); snapshot.Phase != gordis.PhaseReady {
		t.Fatalf("recursive leaf: %+v", snapshot)
	}
	if err := root.Unmount(deadline(t)); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	want := []string{"root/next/next", "root/next", "root"}
	if fmt.Sprint(state.cleaned) != fmt.Sprint(want) {
		t.Fatalf("cleanup order = %v, want %v", state.cleaned, want)
	}
}
