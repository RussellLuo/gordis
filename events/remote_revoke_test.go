package events_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
)

func TestRemoteRevokeKeepsActualHandlerLeased(t *testing.T) {
	f := newRemoteFixture()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	p := &fixturePlugin{id: "subscriber", requires: []gordis.ServiceSpec{events.BusKey.Spec()}, activate: func(_ context.Context, s *gordis.Scope) error {
		_, err := events.Bind(s).On(events.NewTopic[int]("revocation"), func(context.Context, int) error { close(entered); <-release; return nil })
		return err
	}}
	host := remoteHost(t, f, p)
	instance, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "sub", Plugin: p.id})
	if err != nil {
		t.Fatal(err)
	}
	recv(t, f.ready)
	done := make(chan error, 1)
	go func() { _, err := f.endpoint.Deliver(context.Background(), 1, 1, nil); done <- err }()
	recv(t, entered)
	f.endpoint.RevokeSubscription(1)
	f.endpoint.RevokeSubscription(1)
	if _, err := f.endpoint.Deliver(context.Background(), 1, 2, nil); !errors.Is(err, gordis.ErrStale) {
		t.Fatal("revoked delivery admitted", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- instance.Unmount(testContext(t)) }()
	select {
	case err := <-stopped:
		t.Fatal("revocation released actual handler", err)
	default:
	}
	unblock()
	if err := recv(t, done); err != nil {
		t.Fatal(err)
	}
	if err := recv(t, stopped); err != nil {
		t.Fatal(err)
	}
	if f.removed.Load() != 1 {
		t.Fatal("final cleanup did not confirm Host removal", f.removed.Load())
	}
}
