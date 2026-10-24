package events_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
)

type remoteFixture struct {
	mu             sync.Mutex
	next           uint64
	subs           map[uint64]events.RemoteSubscription
	owners         map[events.OwnerRef]string
	endpoint       events.RemoteEndpoint
	subscribe      func(context.Context, events.RemoteSubscription) error
	commit         func(context.Context, uint64) error
	register       func(context.Context, events.OwnerRef) error
	ready          chan events.OwnerRef
	removed        atomic.Int32
	failRemove     atomic.Bool
	afterSubscribe func(uint64)
}

func newRemoteFixture() *remoteFixture {
	f := &remoteFixture{subs: map[uint64]events.RemoteSubscription{}, owners: map[events.OwnerRef]string{}, ready: make(chan events.OwnerRef, 16)}
	f.endpoint = events.NewRemoteBus(f)
	return f
}
func (f *remoteFixture) Subscribe(ctx context.Context, sub events.RemoteSubscription) (uint64, error) {
	if f.subscribe != nil {
		if err := f.subscribe(ctx, sub); err != nil {
			return 0, err
		}
	}
	f.mu.Lock()
	if f.owners[sub.Owner] == "retired" {
		f.mu.Unlock()
		return 0, gordis.ErrClosed
	}
	f.next++
	id := f.next
	f.subs[id] = sub
	f.mu.Unlock()
	if f.afterSubscribe != nil {
		f.afterSubscribe(id)
	}
	return id, nil
}
func (f *remoteFixture) Unsubscribe(_ context.Context, id uint64) error {
	f.removed.Add(1)
	if f.failRemove.Load() {
		return errors.New("unsubscribe failed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subs, id)
	return nil
}
func (f *remoteFixture) CommitSubscription(ctx context.Context, id uint64) error {
	if f.commit != nil {
		return f.commit(ctx, id)
	}
	return nil
}
func (f *remoteFixture) Dispatch(_ context.Context, d events.RemoteDispatch) (events.RemoteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[d.Owner] != "ready" {
		return events.RemoteResult{}, gordis.ErrNotReady
	}
	return events.RemoteResult{}, nil
}
func (f *remoteFixture) RegisterOwner(ctx context.Context, o events.OwnerRef) error {
	if f.register != nil {
		if err := f.register(ctx, o); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[o] == "retired" {
		return gordis.ErrClosed
	}
	f.owners[o] = "registered"
	return nil
}
func (f *remoteFixture) OwnerReady(_ context.Context, o events.OwnerRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[o] == "retired" {
		return gordis.ErrClosed
	}
	f.owners[o] = "ready"
	f.ready <- o
	return nil
}
func (f *remoteFixture) RetireOwner(_ context.Context, o events.OwnerRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners[o] = "retired"
	for id, s := range f.subs {
		if s.Owner == o {
			delete(f.subs, id)
		}
	}
	return nil
}
func remoteHost(t *testing.T, f *remoteFixture, p *fixturePlugin) *gordis.Host {
	t.Helper()
	provider := &fixturePlugin{id: "remote-bus", provides: []gordis.ServiceSpec{events.BusKey.Spec()}, activate: func(_ context.Context, s *gordis.Scope) error {
		return s.Provide(events.BusKey, events.Bus(f.endpoint))
	}}
	host, err := gordis.NewHost(provider, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	if _, err = host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "bus", Plugin: "remote-bus"}); err != nil {
		t.Fatal(err)
	}
	return host
}
func TestRemoteSubscriptionCommitAndOnce(t *testing.T) {
	f := newRemoteFixture()
	topic := events.NewTopic[int]("remote-once")
	var scope *gordis.Scope
	var calls atomic.Int32
	var id uint64
	f.commit = func(ctx context.Context, hostID uint64) error {
		id = hostID
		// Handler is already installed; Ready alone rejects this attempt.
		_, err := f.endpoint.Deliver(ctx, hostID, 1, nil)
		if !errors.Is(err, gordis.ErrNotReady) {
			t.Errorf("delivery before Ready: %v", err)
		}
		return nil
	}
	p := &fixturePlugin{id: "subscriber", requires: []gordis.ServiceSpec{events.BusKey.Spec()}, activate: func(_ context.Context, s *gordis.Scope) error {
		scope = s
		_, err := events.Bind(s).On(topic, func(context.Context, int) error { calls.Add(1); return nil }, events.Once())
		return err
	}}
	host := remoteHost(t, f, p)
	if _, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "sub", Plugin: p.id}); err != nil {
		t.Fatal(err)
	}
	owner := recv(t, f.ready)
	if owner.Instance != scope.ID() || owner.Generation != scope.Generation() {
		t.Fatal(owner)
	}
	if err := events.Bind(scope).Publish(context.Background(), topic, 1); err != nil {
		t.Fatalf("dispatch owner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.endpoint.Deliver(ctx, id, 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := f.endpoint.Deliver(context.Background(), id, "wrong", nil); err == nil {
		t.Fatal("wrong type accepted")
	}
	if _, err := f.endpoint.Deliver(context.Background(), id, nil, nil); err == nil {
		t.Fatal("nil int accepted")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.endpoint.Deliver(context.Background(), id, 1, nil)
			if err != nil && !errors.Is(err, gordis.ErrStale) {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 || f.removed.Load() != 1 {
		t.Fatalf("once calls=%d removals=%d", calls.Load(), f.removed.Load())
	}
	f.mu.Lock()
	remaining := len(f.subs)
	f.mu.Unlock()
	if remaining != 0 {
		t.Fatal("remote Once entry leaked")
	}
	// A runtime subscription must be callable from inside Commit: the local
	// handler is installed before the Host enables its proxy.
	f.commit = func(ctx context.Context, id uint64) error { _, err := f.endpoint.Deliver(ctx, id, 2, nil); return err }
	remove, err := events.Bind(scope).On(topic, func(context.Context, int) error { calls.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	remove()
	if calls.Load() != 2 {
		t.Fatal("runtime handler not installed before Commit")
	}
	if err := host.Shutdown(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestRemoteCommitFailureAndCleanup(t *testing.T) {
	for _, failRemove := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "retry-unsubscribe"}[failRemove], func(t *testing.T) {
			f := newRemoteFixture()
			f.failRemove.Store(failRemove)
			var scope *gordis.Scope
			p := &fixturePlugin{id: "subscriber", requires: []gordis.ServiceSpec{events.BusKey.Spec()}, activate: func(_ context.Context, s *gordis.Scope) error { scope = s; return nil }}
			host := remoteHost(t, f, p)
			if _, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "sub", Plugin: p.id}); err != nil {
				t.Fatal(err)
			}
			f.commit = func(context.Context, uint64) error { return errors.New("lost Commit response") }
			remove, err := events.Bind(scope).On(events.NewTopic[int]("failed"), func(context.Context, int) error { t.Error("failed subscription ran"); return nil })
			if err == nil || remove != nil {
				t.Fatal("Commit failure returned success")
			}
			if _, err = f.endpoint.Deliver(context.Background(), 1, 1, nil); !errors.Is(err, gordis.ErrStale) {
				t.Fatal(err)
			}
			f.failRemove.Store(false)
			if err = host.Shutdown(testContext(t)); err != nil {
				t.Fatal(err)
			}
			if f.removed.Load() != map[bool]int32{false: 1, true: 2}[failRemove] {
				t.Fatalf("cleanup retries: %d", f.removed.Load())
			}
		})
	}
}
func TestRemoteOwnerRegistrationStopRace(t *testing.T) {
	f := newRemoteFixture()
	entered, release, retired := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	f.register = func(context.Context, events.OwnerRef) error { close(entered); <-release; return nil }
	var scope *gordis.Scope
	p := &fixturePlugin{id: "subscriber", requires: []gordis.ServiceSpec{events.BusKey.Spec()}, activate: func(_ context.Context, s *gordis.Scope) error {
		scope = s
		return s.OnStop("test retirement observed", func(context.Context) error { close(retired); return nil })
	}}
	host := remoteHost(t, f, p)
	instance, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "sub", Plugin: p.id})
	if err != nil {
		t.Fatal(err)
	}
	registered := make(chan error, 1)
	go func() {
		_, err := events.Bind(scope).On(events.NewTopic[int]("race"), func(context.Context, int) error { return nil })
		registered <- err
	}()
	recv(t, entered)
	stopped := make(chan error, 1)
	go func() { stopped <- instance.Unmount(testContext(t)) }()
	recv(t, retired)
	unblock()
	if err = recv(t, registered); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if err = recv(t, stopped); err != nil {
		t.Fatal(err)
	}
	select {
	case owner := <-f.ready:
		t.Fatalf("retired owner became Ready: %+v", owner)
	default:
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.subs) != 0 {
		t.Fatal("retired owner leaked subscriptions")
	}
}

func TestRemoteSubscribeStopRace(t *testing.T) {
	f := newRemoteFixture()
	entered, release, retired := make(chan uint64, 1), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.afterSubscribe = func(id uint64) { entered <- id; <-release }
	var scope *gordis.Scope
	p := &fixturePlugin{id: "subscriber", requires: []gordis.ServiceSpec{events.BusKey.Spec()}, activate: func(_ context.Context, s *gordis.Scope) error {
		scope = s
		return s.OnStop("retirement observed", func(context.Context) error { close(retired); return nil })
	}}
	host := remoteHost(t, f, p)
	instance, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "sub", Plugin: p.id})
	if err != nil {
		t.Fatal(err)
	}
	registered := make(chan error, 1)
	go func() {
		_, err := events.Bind(scope).On(events.NewTopic[int]("late-response"), func(context.Context, int) error { t.Error("late handler ran"); return nil })
		registered <- err
	}()
	id := recv(t, entered)
	stopped := make(chan error, 1)
	go func() { stopped <- instance.Unmount(testContext(t)) }()
	recv(t, retired)
	select {
	case err := <-stopped:
		t.Fatalf("cleanup passed pending Subscribe: %v", err)
	default:
	}
	unblock()
	if err = recv(t, registered); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if err = recv(t, stopped); err != nil {
		t.Fatal(err)
	}
	if _, err = f.endpoint.Deliver(context.Background(), id, 1, nil); !errors.Is(err, gordis.ErrStale) {
		t.Fatal(err)
	}
	if f.removed.Load() != 1 {
		t.Fatalf("late response not revoked: %d", f.removed.Load())
	}
}

func TestRemoteUnsubscribeResidual(t *testing.T) {
	f := newRemoteFixture()
	var scope *gordis.Scope
	p := &fixturePlugin{id: "subscriber", requires: []gordis.ServiceSpec{events.BusKey.Spec()}, activate: func(_ context.Context, s *gordis.Scope) error { scope = s; return nil }}
	host := remoteHost(t, f, p)
	if _, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "sub", Plugin: p.id}); err != nil {
		t.Fatal(err)
	}
	remove, err := events.Bind(scope).On(events.NewTopic[int]("residual"), func(context.Context, int) error { t.Error("removed handler ran"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.failRemove.Store(true)
	remove()
	if _, err = f.endpoint.Deliver(context.Background(), 1, 1, nil); !errors.Is(err, gordis.ErrStale) {
		t.Fatal(err)
	}
	if err = host.Shutdown(testContext(t)); err == nil {
		t.Fatal("unconfirmed unsubscribe lost cleanup failure")
	}
	retained := false
	for _, node := range host.Snapshot() {
		if node.LocalID == "sub" && len(node.Residuals) != 0 {
			retained = true
		}
	}
	if !retained {
		t.Fatal("unconfirmed unsubscribe lost its Scope residual")
	}
}
