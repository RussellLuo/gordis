package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
)

type fixturePlugin struct {
	id                 string
	requires, provides []gordis.ServiceSpec
	activate           func(context.Context, *gordis.Scope) error
}

func (p *fixturePlugin) Spec() gordis.PluginSpec {
	template := *p
	return gordis.PluginSpec{
		ID: p.id, Requires: p.requires, Provides: p.provides,
		New: func() gordis.Plugin {
			copy := template
			return &copy
		},
	}
}

func (p *fixturePlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	if p.activate == nil {
		return nil
	}
	return p.activate(ctx, scope)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newHost(t *testing.T, plugins ...gordis.Plugin) *gordis.Host {
	t.Helper()
	all := append([]gordis.Plugin{new(events.Plugin)}, plugins...)
	host, err := gordis.NewHost(all)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	bus, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{
		ID: "events", Plugin: events.PluginID, Config: json.RawMessage(`{"maxParallel":1}`),
	})
	if err != nil || bus == nil {
		t.Fatalf("mount bus: %v", err)
	}
	return host
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-testContext(t).Done():
		t.Fatal("timed out")
		var zero T
		return zero
	}
}

func TestReadyGateOncePriorityAndDrain(t *testing.T) {
	topic := events.NewTopic[string]("ready")
	registered := make(chan struct{})
	finishActivation := make(chan struct{})
	entered := make(chan struct{}, 4)
	releaseHandler := make(chan struct{})
	var orderMu sync.Mutex
	var order []string
	subscriber := &fixturePlugin{
		id: "subscriber", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			binding := events.Bind(scope)
			if _, err := binding.On(topic, func(context.Context, string) error {
				orderMu.Lock()
				order = append(order, "low")
				orderMu.Unlock()
				entered <- struct{}{}
				<-releaseHandler
				return nil
			}); err != nil {
				return err
			}
			if _, err := binding.On(topic, func(context.Context, string) error {
				orderMu.Lock()
				order = append(order, "once")
				orderMu.Unlock()
				return nil
			}, events.Priority(10), events.Once()); err != nil {
				return err
			}
			close(registered)
			<-finishActivation
			return nil
		},
	}
	var publisherScope *gordis.Scope
	publisher := &fixturePlugin{
		id: "publisher", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			publisherScope = scope
			return nil
		},
	}
	host := newHost(t, subscriber, publisher)
	if _, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "publisher", Plugin: "publisher"}); err != nil {
		t.Fatal(err)
	}
	sub, err := host.Env().Mount(testContext(t), gordis.InstanceSpec{ID: "subscriber", Plugin: "subscriber"})
	if err != nil {
		t.Fatal(err)
	}
	recv(t, registered)
	if err := events.Bind(publisherScope).Emit(testContext(t), topic, "before-ready"); err != nil {
		t.Fatal(err)
	}
	orderMu.Lock()
	if len(order) != 0 {
		t.Fatalf("pre-Ready delivery: %v", order)
	}
	orderMu.Unlock()
	close(finishActivation)
	if err := sub.WaitReady(testContext(t)); err != nil {
		t.Fatal(err)
	}

	publishDone := make(chan error, 1)
	go func() { publishDone <- events.Bind(publisherScope).Emit(context.Background(), topic, "ready") }()
	recv(t, entered)
	orderMu.Lock()
	if !reflect.DeepEqual(order, []string{"once", "low"}) {
		t.Fatalf("priority order: %v", order)
	}
	orderMu.Unlock()
	unmountDone := make(chan error, 1)
	go func() { unmountDone <- sub.Unmount(context.Background()) }()
	select {
	case err := <-unmountDone:
		t.Fatalf("unmount did not drain handler: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := events.Bind(publisherScope).Emit(testContext(t), topic, "stopping"); err != nil {
		t.Fatal(err)
	}
	close(releaseHandler)
	if err := recv(t, publishDone); err != nil {
		t.Fatal(err)
	}
	if err := recv(t, unmountDone); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingSourceGlobalAndTopicPartitions(t *testing.T) {
	topic := events.NewTopic[string]("orders")
	sourceKey := gordis.NewKey[int]("order.writer")
	counts := map[string]*atomic.Int32{
		"a": {}, "b": {}, "global": {}, "other-topic": {},
	}
	newSubscriber := func(id string, count *atomic.Int32, options ...events.OnOption) gordis.Plugin {
		return &fixturePlugin{
			id: id, requires: []gordis.ServiceSpec{events.BusKey.Spec()},
			activate: func(_ context.Context, scope *gordis.Scope) error {
				_, err := events.Bind(scope).On(topic, func(context.Context, string) error {
					count.Add(1)
					return nil
				}, options...)
				return err
			},
		}
	}
	var writerScope *gordis.Scope
	writer := &fixturePlugin{
		id: "writer", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		provides: []gordis.ServiceSpec{sourceKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			writerScope = scope
			return gordis.Provide(scope, sourceKey, 1)
		},
	}
	host := newHost(t,
		writer,
		newSubscriber("sub-a", counts["a"]),
		newSubscriber("sub-b", counts["b"]),
		newSubscriber("sub-global", counts["global"], events.Global()),
		newSubscriber("sub-other-topic", counts["other-topic"], events.Global()),
	)
	tenantA := host.Env().Isolate(topic, "topic-a").Isolate(sourceKey, "source-a")
	if _, err := tenantA.MountReady(testContext(t), gordis.InstanceSpec{ID: "writer", Plugin: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenantA.MountReady(testContext(t), gordis.InstanceSpec{ID: "sub-a", Plugin: "sub-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Env().Isolate(topic, "topic-a").Isolate(sourceKey, "source-b").
		MountReady(testContext(t), gordis.InstanceSpec{ID: "sub-b", Plugin: "sub-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Env().Isolate(topic, "topic-a").Isolate(sourceKey, "source-b").
		MountReady(testContext(t), gordis.InstanceSpec{ID: "sub-global", Plugin: "sub-global"}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Env().Isolate(topic, "topic-b").Isolate(sourceKey, "source-a").
		MountReady(testContext(t), gordis.InstanceSpec{ID: "sub-other-topic", Plugin: "sub-other-topic"}); err != nil {
		t.Fatal(err)
	}
	if err := events.Bind(writerScope).PublishFrom(testContext(t), sourceKey, topic, "created"); err != nil {
		t.Fatal(err)
	}
	if got := []int32{counts["a"].Load(), counts["b"].Load(), counts["global"].Load(), counts["other-topic"].Load()}; !reflect.DeepEqual(got, []int32{1, 0, 1, 0}) {
		t.Fatalf("routing counts: %v", got)
	}
}

func TestSelectionWaterfallPanicErrorsAndBackpressure(t *testing.T) {
	notice := events.NewTopic[int]("parallel")
	cancelNotice := events.NewTopic[int]("parallel-cancel")
	onceNotice := events.NewTopic[int]("once")
	choose := events.NewHook[string, string]("choose")
	transform := events.NewHook[string, string]("transform")
	twice := events.NewHook[string, string]("twice")
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	cancelFirstEntered := make(chan struct{})
	cancelRelease := make(chan struct{})
	var cancelSecondCalls atomic.Int32
	var onceCalls atomic.Int32
	var scopeRef *gordis.Scope
	plugin := &fixturePlugin{
		id: "extension", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			scopeRef = scope
			binding := events.Bind(scope)
			if _, err := binding.On(notice, func(context.Context, int) error {
				close(firstEntered)
				<-releaseFirst
				return errors.New("first failed")
			}, events.Priority(10)); err != nil {
				return err
			}
			if _, err := binding.On(notice, func(context.Context, int) error {
				close(secondEntered)
				panic("second panic")
			}); err != nil {
				return err
			}
			if _, err := binding.On(onceNotice, func(context.Context, int) error {
				onceCalls.Add(1)
				return nil
			}, events.Once()); err != nil {
				return err
			}
			if _, err := binding.Handle(choose, func(context.Context, string) (string, bool, error) {
				return "ignored", false, nil
			}, events.Priority(10)); err != nil {
				return err
			}
			if _, err := binding.Handle(choose, func(_ context.Context, in string) (string, bool, error) {
				return in + "!", true, nil
			}); err != nil {
				return err
			}
			if _, err := binding.On(cancelNotice, func(context.Context, int) error {
				close(cancelFirstEntered)
				<-cancelRelease
				return nil
			}, events.Priority(10)); err != nil {
				return err
			}
			if _, err := binding.On(cancelNotice, func(context.Context, int) error {
				cancelSecondCalls.Add(1)
				return nil
			}); err != nil {
				return err
			}
			if _, err := binding.Use(transform, func(ctx context.Context, in string, next events.Next[string, string]) (string, error) {
				out, err := next(ctx, in+"-outer")
				return "[" + out + "]", err
			}, events.Priority(10)); err != nil {
				return err
			}
			if _, err := binding.Use(transform, func(ctx context.Context, in string, next events.Next[string, string]) (string, error) {
				return next(ctx, in+"-inner")
			}); err != nil {
				return err
			}
			_, err := binding.Use(twice, func(ctx context.Context, in string, next events.Next[string, string]) (string, error) {
				if _, err := next(ctx, in); err != nil {
					return "", err
				}
				return next(ctx, in)
			})
			return err
		},
	}
	host := newHost(t, plugin)
	if _, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "extension", Plugin: "extension"}); err != nil {
		t.Fatal(err)
	}
	binding := events.Bind(scopeRef)
	if err := binding.Emit(testContext(t), onceNotice, 1); err != nil {
		t.Fatal(err)
	}
	if err := binding.Emit(testContext(t), onceNotice, 2); err != nil {
		t.Fatal(err)
	}
	if onceCalls.Load() != 1 {
		t.Fatalf("Once calls: %d", onceCalls.Load())
	}
	result, handled, err := binding.Serial(testContext(t), choose, "pick")
	if err != nil || !handled || result != "pick!" {
		t.Fatalf("selection: %q %v %v", result, handled, err)
	}
	missing := events.NewHook[string, string]("missing")
	if _, handled, err := binding.Bail(testContext(t), missing, "x"); handled || !errors.Is(err, events.ErrNoHandler) {
		t.Fatalf("missing handler: handled=%v err=%v", handled, err)
	}
	out, err := binding.Waterfall(testContext(t), transform, "start", func(_ context.Context, in string) (string, error) {
		return in + "-base", nil
	})
	if err != nil || out != "[start-outer-inner-base]" {
		t.Fatalf("waterfall: %q %v", out, err)
	}
	if _, err := binding.Waterfall(testContext(t), twice, "x", func(_ context.Context, in string) (string, error) {
		return in, nil
	}); !errors.Is(err, events.ErrNextCalled) {
		t.Fatalf("double next: %v", err)
	}

	parallelDone := make(chan error, 1)
	go func() { parallelDone <- binding.Parallel(context.Background(), notice, 1) }()
	recv(t, firstEntered)
	select {
	case <-secondEntered:
		t.Fatal("parallelism limit was bypassed")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	recv(t, secondEntered)
	err = recv(t, parallelDone)
	if err == nil || !strings.Contains(err.Error(), "first failed") || !strings.Contains(err.Error(), "handler panic: second panic") {
		t.Fatalf("parallel errors: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- binding.Parallel(cancelCtx, cancelNotice, 1) }()
	recv(t, cancelFirstEntered)
	cancel()
	close(cancelRelease)
	if err := recv(t, cancelDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("parallel cancellation: %v", err)
	}
	if cancelSecondCalls.Load() != 0 {
		t.Fatalf("backpressured handler ran after cancellation: %d", cancelSecondCalls.Load())
	}
}

func TestWaterfallNextExpiresWhenMiddlewareReturns(t *testing.T) {
	hook := events.NewHook[string, string]("late-next")
	saved := make(chan events.Next[string, string], 1)
	var scopeRef *gordis.Scope
	plugin := &fixturePlugin{
		id: "late-next", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			scopeRef = scope
			_, err := events.Bind(scope).Use(hook, func(
				_ context.Context, _ string, next events.Next[string, string],
			) (string, error) {
				saved <- next
				return "short-circuit", nil
			})
			return err
		},
	}
	host := newHost(t, plugin)
	instance, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "late-next", Plugin: "late-next"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := events.Bind(scopeRef).Waterfall(testContext(t), hook, "start", func(
		_ context.Context, input string,
	) (string, error) {
		return input + "-base", nil
	})
	if err != nil || out != "short-circuit" {
		t.Fatalf("waterfall: %q %v", out, err)
	}
	next := recv(t, saved)
	if err := instance.Unmount(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := next(context.Background(), "late"); !errors.Is(err, events.ErrNextExpired) {
		t.Fatalf("late next: %v", err)
	}
}

func TestWaterfallDrainsNextAdmittedBeforeMiddlewareReturns(t *testing.T) {
	hook := events.NewHook[string, string]("in-flight-next")
	nextEntered := make(chan struct{})
	releaseBase := make(chan struct{})
	nextDone := make(chan error, 1)
	var scopeRef *gordis.Scope
	plugin := &fixturePlugin{
		id: "in-flight-next", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			scopeRef = scope
			_, err := events.Bind(scope).Use(hook, func(
				ctx context.Context, input string, next events.Next[string, string],
			) (string, error) {
				go func() {
					_, err := next(ctx, input)
					nextDone <- err
				}()
				<-nextEntered
				return "middleware-returned", nil
			})
			return err
		},
	}
	host := newHost(t, plugin)
	if _, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{
		ID: "in-flight-next", Plugin: "in-flight-next",
	}); err != nil {
		t.Fatal(err)
	}

	waterfallDone := make(chan error, 1)
	go func() {
		out, err := events.Bind(scopeRef).Waterfall(context.Background(), hook, "start", func(
			_ context.Context, input string,
		) (string, error) {
			close(nextEntered)
			<-releaseBase
			return input + "-base", nil
		})
		if err == nil && out != "middleware-returned" {
			err = fmt.Errorf("waterfall result = %q", out)
		}
		waterfallDone <- err
	}()
	recv(t, nextEntered)
	select {
	case err := <-waterfallDone:
		close(releaseBase)
		t.Fatalf("dispatch returned before admitted next drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseBase)
	if err := recv(t, waterfallDone); err != nil {
		t.Fatal(err)
	}
	if err := recv(t, nextDone); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationValidationAndPublishBeforeReady(t *testing.T) {
	topic := events.NewTopic[string]("validation")
	invalidTopic := events.NewTopic[string]("")
	registered := make(chan error, 1)
	publishErr := make(chan error, 1)
	finish := make(chan struct{})
	plugin := &fixturePlugin{
		id: "validation", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			binding := events.Bind(scope)
			_, err := binding.On(invalidTopic, func(context.Context, string) error { return nil })
			registered <- err
			publishErr <- binding.Publish(context.Background(), topic, "too-soon")
			<-finish
			return nil
		},
	}
	host := newHost(t, plugin)
	instance, err := host.Env().Mount(testContext(t), gordis.InstanceSpec{ID: "validation", Plugin: "validation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := recv(t, registered); err == nil {
		t.Fatal("invalid contract accepted")
	}
	if err := recv(t, publishErr); !errors.Is(err, gordis.ErrNotReady) {
		t.Fatalf("pre-Ready publish: %v", err)
	}
	close(finish)
	if err := instance.WaitReady(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRegistrationAndStopLeavesNoSubscription(t *testing.T) {
	topic := events.NewTopic[int]("registration-stop")
	var subscriberScope, publisherScope *gordis.Scope
	var calls atomic.Int32
	subscriber := &fixturePlugin{
		id: "subscriber-race", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			subscriberScope = scope
			return nil
		},
	}
	publisher := &fixturePlugin{
		id: "publisher-race", requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		activate: func(_ context.Context, scope *gordis.Scope) error {
			publisherScope = scope
			return nil
		},
	}
	host := newHost(t, subscriber, publisher)
	if _, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "publisher-race", Plugin: "publisher-race"}); err != nil {
		t.Fatal(err)
	}
	instance, err := host.Env().MountReady(testContext(t), gordis.InstanceSpec{ID: "subscriber-race", Plugin: "subscriber-race"})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 50 {
				cancel, err := events.Bind(subscriberScope).On(topic, func(context.Context, int) error {
					calls.Add(1)
					return nil
				})
				if err != nil {
					if !errors.Is(err, gordis.ErrClosed) {
						t.Errorf("register during stop: %v", err)
					}
					return
				}
				cancel()
			}
		}()
	}
	close(start)
	if err := instance.Unmount(testContext(t)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := events.Bind(subscriberScope).Emit(testContext(t), topic, 1); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("old generation publish: %v", err)
	}
	before := calls.Load()
	if err := events.Bind(publisherScope).Emit(testContext(t), topic, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatalf("subscription survived stop: before=%d after=%d", before, calls.Load())
	}
}
