package gordis_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
)

type p2Value interface {
	Value() string
}

type p2ValueString string

func (v p2ValueString) Value() string { return string(v) }

var p2ValueKey = gordis.NewKey[p2Value]("p2.value")

type p2Provider struct {
	ValueText string `json:"value"`
}

func (*p2Provider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "p2-provider",
		Provides: []gordis.ServiceSpec{p2ValueKey.Spec()},
		New:      func() gordis.Plugin { return new(p2Provider) },
	}
}

func (p *p2Provider) Activate(_ context.Context, scope *gordis.Scope) error {
	var value p2Value = p2ValueString(p.ValueText)
	return gordis.Provide(scope, p2ValueKey, value)
}

type p2Consumer struct {
	values chan<- string
}

func (p *p2Consumer) Spec() gordis.PluginSpec {
	values := p.values
	return gordis.PluginSpec{
		ID:       "p2-consumer",
		Requires: []gordis.ServiceSpec{p2ValueKey.Spec()},
		New:      func() gordis.Plugin { return &p2Consumer{values: values} },
	}
}

func (p *p2Consumer) Activate(_ context.Context, scope *gordis.Scope) error {
	value, err := gordis.Get(scope, p2ValueKey)
	if err != nil {
		return err
	}
	p.values <- value.Value()
	return nil
}

func p2Snapshot(t *testing.T, host *gordis.Host, id string) gordis.Snapshot {
	t.Helper()
	for _, snapshot := range host.Snapshot() {
		if snapshot.QualifiedID == id {
			return snapshot
		}
	}
	t.Fatalf("missing snapshot %q", id)
	return gordis.Snapshot{}
}

func p2Deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRootEnvPendingRecoveryUpdateRestartAndUnmount(t *testing.T) {
	values := make(chan string, 4)
	host, err := gordis.NewHost([]gordis.Plugin{new(p2Provider), &p2Consumer{values: values}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })

	consumer, err := host.Env().Mount(context.Background(), gordis.InstanceSpec{
		ID: "consumer", Plugin: "p2-consumer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := p2Snapshot(t, host, "consumer"); snapshot.Phase != gordis.PhasePending {
		t.Fatalf("consumer was not pending: %+v", snapshot)
	}

	provider, err := host.Env().MountReady(p2Deadline(t), gordis.InstanceSpec{
		ID: "provider", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"one"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.WaitReady(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if value := <-values; value != "one" {
		t.Fatalf("first binding value = %q", value)
	}

	before := p2Snapshot(t, host, "provider").Generation
	if err := provider.Update(p2Deadline(t), json.RawMessage(`{"value":"two"}`)); err != nil {
		t.Fatal(err)
	}
	if err := provider.WaitReady(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if err := consumer.WaitReady(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if value := <-values; value != "two" {
		t.Fatalf("updated binding value = %q", value)
	}
	if after := p2Snapshot(t, host, "provider").Generation; after <= before {
		t.Fatalf("provider generation did not advance: %d -> %d", before, after)
	}

	before = p2Snapshot(t, host, "provider").Generation
	if err := provider.Restart(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if err := provider.WaitReady(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if err := consumer.WaitReady(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if value := <-values; value != "two" {
		t.Fatalf("restarted binding value = %q", value)
	}
	if after := p2Snapshot(t, host, "provider").Generation; after <= before {
		t.Fatalf("restart did not advance generation: %d -> %d", before, after)
	}

	if err := provider.Unmount(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if snapshot := p2Snapshot(t, host, "consumer"); snapshot.Phase != gordis.PhasePending {
		t.Fatalf("consumer after provider unmount = %+v", snapshot)
	}
	if err := consumer.Unmount(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if err := provider.WaitReady(context.Background()); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("unmounted handle error = %v", err)
	}
}

func TestRootEnvViewIsolationAndMountReadyFailureHandle(t *testing.T) {
	values := make(chan string, 2)
	host, err := gordis.NewHost([]gordis.Plugin{new(p2Provider), &p2Consumer{values: values}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })

	tenant := host.Env().Isolate(p2ValueKey, "tenant-a")
	consumer, err := tenant.Mount(context.Background(), gordis.InstanceSpec{
		ID: "tenant-consumer", Plugin: "p2-consumer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Env().MountReady(p2Deadline(t), gordis.InstanceSpec{
		ID: "default-provider", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"default"}`),
	}); err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := consumer.WaitReady(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("isolated consumer unexpectedly resolved: %v", err)
	}
	if snapshot := p2Snapshot(t, host, "tenant-consumer"); len(snapshot.Bindings) != 1 || snapshot.Bindings[0].Label != "tenant-a" {
		t.Fatalf("wrong isolated binding: %+v", snapshot.Bindings)
	}

	if _, err := tenant.MountReady(p2Deadline(t), gordis.InstanceSpec{
		ID: "tenant-provider", Plugin: "p2-provider", Config: json.RawMessage(`{"value":"tenant"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := consumer.WaitReady(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if value := <-values; value != "tenant" {
		t.Fatalf("isolated value = %q", value)
	}

	failing, err := host.Env().MountReady(p2Deadline(t), gordis.InstanceSpec{
		ID: "bad", Plugin: "p2-provider", Config: json.RawMessage(`{"value":`),
	})
	if err == nil || failing != nil {
		t.Fatalf("preflight failure committed: instance=%v err=%v", failing, err)
	}
}

type p2Failing struct{}

func (*p2Failing) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "p2-failing", New: func() gordis.Plugin { return new(p2Failing) }}
}

func (*p2Failing) Activate(context.Context, *gordis.Scope) error {
	return fmt.Errorf("p2 activation failed")
}

type p2MissingActivate struct{}

func (*p2MissingActivate) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "p2-missing-activate", New: func() gordis.Plugin { return new(p2MissingActivate) }}
}

type p2FactoryMissingActivatePrototype struct{}

type p2FactoryMissingActivateResult struct{}

func p2FactoryMissingActivateSpec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:  "p2-factory-missing-activate",
		New: func() gordis.Plugin { return new(p2FactoryMissingActivateResult) },
	}
}

func (*p2FactoryMissingActivatePrototype) Spec() gordis.PluginSpec {
	return p2FactoryMissingActivateSpec()
}

func (*p2FactoryMissingActivatePrototype) Activate(context.Context, *gordis.Scope) error {
	return nil
}

func (*p2FactoryMissingActivateResult) Spec() gordis.PluginSpec {
	return p2FactoryMissingActivateSpec()
}

func TestMountReadyKeepsCommittedFailureAndShutdownClosesEnv(t *testing.T) {
	if _, err := gordis.NewHost([]gordis.Plugin{new(p2MissingActivate)}); err == nil {
		t.Fatal("plugin without Activate was registered")
	}
	host, err := gordis.NewHost([]gordis.Plugin{new(p2Failing)})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := host.Env().MountReady(p2Deadline(t), gordis.InstanceSpec{ID: "bad", Plugin: "p2-failing"})
	if instance == nil || err == nil || err.Error() != "p2 activation failed" {
		t.Fatalf("instance=%v err=%v", instance, err)
	}
	if err := host.Shutdown(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Env().Mount(context.Background(), gordis.InstanceSpec{ID: "later", Plugin: "p2-failing"}); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("closed root Env accepted mount: %v", err)
	}
}

func TestMountPreflightRejectsFactoryResultWithoutActivate(t *testing.T) {
	host, err := gordis.NewHost([]gordis.Plugin{new(p2FactoryMissingActivatePrototype)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })

	instance, err := host.Env().Mount(context.Background(), gordis.InstanceSpec{
		ID: "broken", Plugin: "p2-factory-missing-activate",
	})
	if err == nil || instance != nil || !strings.Contains(err.Error(), "factory result does not implement Activate") {
		t.Fatalf("instance=%v err=%v", instance, err)
	}
	if snapshots := host.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("invalid factory result committed: %+v", snapshots)
	}
}

func TestUnmountDeadlineBoundsCallerWhileAcceptedCleanupContinues(t *testing.T) {
	scopes := make(chan *gordis.Scope, 1)
	plugin := definition("p2-leased", func(_ context.Context, scope *gordis.Scope) error {
		scopes <- scope
		return nil
	})
	host, err := gordis.NewHost([]gordis.Plugin{plugin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	instance, err := host.Env().MountReady(p2Deadline(t), gordis.InstanceSpec{ID: "leased", Plugin: "p2-leased"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := (<-scopes).Acquire()
	if err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- instance.Unmount(short) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			release()
			t.Fatalf("Unmount deadline error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		release()
		<-done
		t.Fatal("Unmount ignored its context deadline")
	}

	release()
	if err := host.Shutdown(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	if snapshots := host.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("accepted cleanup did not finish: %+v", snapshots)
	}
}

func TestMountCommitBoundaryAndPendingTimeout(t *testing.T) {
	values := make(chan string, 1)
	host, err := gordis.NewHost([]gordis.Plugin{&p2Consumer{values: values}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if instance, err := host.Env().Mount(cancelled, gordis.InstanceSpec{
		ID: "cancelled", Plugin: "p2-consumer",
	}); !errors.Is(err, context.Canceled) || instance != nil {
		t.Fatalf("cancelled mount: instance=%v err=%v", instance, err)
	}
	if snapshots := host.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("cancelled mount committed: %+v", snapshots)
	}

	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	instance, err := host.Env().MountReady(short, gordis.InstanceSpec{
		ID: "pending", Plugin: "p2-consumer",
	})
	if !errors.Is(err, context.DeadlineExceeded) || instance == nil {
		t.Fatalf("pending MountReady: instance=%v err=%v", instance, err)
	}
	if snapshot := p2Snapshot(t, host, "pending"); snapshot.Phase != gordis.PhasePending {
		t.Fatalf("timed-out instance was not retained: %+v", snapshot)
	}
	if err := instance.Unmount(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
}

func TestServiceAddressIncludesKeyAndLabel(t *testing.T) {
	keyA := gordis.NewKey[*int]("address.a")
	keyB := gordis.NewKey[*int]("address.b")
	valueA, valueB := 1, 2

	providerA := definition("provider-a", func(_ context.Context, scope *gordis.Scope) error {
		return gordis.Provide(scope, keyA, &valueA)
	})
	providerA.Provides = []gordis.ServiceSpec{keyA.Spec()}
	providerB := definition("provider-b", func(_ context.Context, scope *gordis.Scope) error {
		return gordis.Provide(scope, keyB, &valueB)
	})
	providerB.Provides = []gordis.ServiceSpec{keyB.Spec()}
	consumer := definition("consumer-both", func(_ context.Context, scope *gordis.Scope) error {
		a, err := gordis.Get(scope, keyA)
		if err != nil {
			return err
		}
		b, err := gordis.Get(scope, keyB)
		if err != nil {
			return err
		}
		if *a != valueA || *b != valueB {
			return fmt.Errorf("unexpected values %d/%d", *a, *b)
		}
		return nil
	})
	consumer.Requires = []gordis.ServiceSpec{keyA.Spec(), keyB.Spec()}

	host, err := gordis.NewHost([]gordis.Plugin{providerA, providerB, consumer})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	env := host.Env().Isolate(keyA, "shared").Isolate(keyB, "shared")
	changes := host.Changes()
	changes.Mount(env, gordis.InstanceSpec{ID: "a", Plugin: "provider-a"})
	changes.Mount(env, gordis.InstanceSpec{ID: "b", Plugin: "provider-b"})
	changes.Mount(env, gordis.InstanceSpec{ID: "consumer", Plugin: "consumer-both"})
	operation, err := changes.Apply(p2Deadline(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.WaitReady(p2Deadline(t)); err != nil {
		t.Fatal(err)
	}
	snapshot := p2Snapshot(t, host, "consumer")
	if len(snapshot.Bindings) != 2 || snapshot.Bindings[0].Label != "shared" || snapshot.Bindings[1].Label != "shared" {
		t.Fatalf("bindings = %+v", snapshot.Bindings)
	}
}

func TestHostRejectsInvalidCatalog(t *testing.T) {
	if _, err := gordis.NewHost([]gordis.Plugin{definition("same", nil), definition("same", nil)}); err == nil {
		t.Fatal("duplicate plugin type was accepted")
	}
	invalid := definition("invalid", nil)
	invalid.Requires = []gordis.ServiceSpec{gordis.NewKey[*int]("").Spec()}
	if _, err := gordis.NewHost([]gordis.Plugin{invalid}); err == nil {
		t.Fatal("invalid service key was accepted")
	}
}
