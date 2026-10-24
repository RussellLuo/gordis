package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func singleCallLimits() Limits {
	return Limits{Business: AdmissionLimit{1, 1}, Control: AdmissionLimit{1, 1}, Continuation: AdmissionLimit{1, 1}}
}
func admissionUsage(s AdmissionSnapshot, class AdmissionClass) AdmissionLimit {
	return []AdmissionLimit{s.Business, s.Control, s.Continuation}[class]
}
func requireBackpressure(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrBackpressure) || status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("want backpressure + status, got %v", err)
	}
}

type blockingJSON struct {
	entered, release chan struct{}
	calls            *atomic.Int32
	err              error
}

func (b blockingJSON) MarshalJSON() ([]byte, error) {
	b.calls.Add(1)
	close(b.entered)
	<-b.release
	return []byte(`{}`), b.err
}
func TestAdmissionZeroAndMixedLimits(t *testing.T) {
	for class := Business; class <= Continuation; class++ {
		for _, boundedPending := range []bool{false, true} {
			t.Run(fmt.Sprintf("class=%d/bounded-pending=%t", class, boundedPending), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				limits := Limits{}
				groups := []*AdmissionLimit{&limits.Business, &limits.Control, &limits.Continuation}
				if boundedPending {
					groups[class].Pending = 1
				} else {
					groups[class].Handling = 1
				}
				a := newAdmission(limits)
				boundedDone, err := a.enter(ctx, class, boundedPending)
				requireNoError(t, err)
				_, err = a.enter(ctx, class, boundedPending)
				requireBackpressure(t, err)
				done := []func(){boundedDone}
				for range 20 {
					release, err := a.enter(ctx, class, !boundedPending)
					requireNoError(t, err)
					done = append(done, release)
				}
				want := AdmissionLimit{20, 1}
				if boundedPending {
					want = AdmissionLimit{1, 20}
				}
				if got := admissionUsage(a.snapshot(), class); got != want {
					t.Fatal("mixed limits not independently counted", got)
				}
				a.close()
				if _, err := a.enter(ctx, class, !boundedPending); !errors.Is(err, gordis.ErrClosed) {
					t.Fatal("unlimited admission stayed open", err)
				}
				drained := make(chan error, 1)
				go func() { a.wait(); drained <- nil }()
				assertNotCompleted(t, drained)
				for _, release := range done {
					release()
				}
				requireNoError(t, receiveResult(t, ctx, drained))
				if a.snapshot() != (AdmissionSnapshot{}) {
					t.Fatal(a.snapshot())
				}
			})
		}
	}
}

func TestAdmissionClassesAreIndependent(t *testing.T) {
	ctx := context.Background()
	a := newAdmission(singleCallLimits())
	var releases []func()
	for class := Business; class <= Continuation; class++ {
		for _, pending := range []bool{false, true} {
			release, err := a.enter(ctx, class, pending)
			requireNoError(t, err)
			releases = append(releases, release)
			_, err = a.enter(ctx, class, pending)
			requireBackpressure(t, err)
		}
	}
	if got := a.snapshot(); got.Business != (AdmissionLimit{1, 1}) || got.Control != got.Business || got.Continuation != got.Business {
		t.Fatalf("classes borrowed capacity: %+v", got)
	}
	a.closePending()
	for _, release := range releases {
		release()
	}
	if _, err := a.enter(ctx, Business, true); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal("outbound admission reopened", err)
	}
	release, err := a.enter(ctx, Business, false)
	requireNoError(t, err)
	release()
	a.close()
	a.wait()
	if a.snapshot() != (AdmissionSnapshot{}) {
		t.Fatal("released work remained counted")
	}
}
func TestInvokerAdmissionBeforeSerialization(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			callCtx, cancelCall := context.WithCancel(ctx)
			defer cancelCall()
			a := newAdmission(singleCallLimits())
			inv := &contractInvoker{admission: a}
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			msg := blockingJSON{entered: entered, release: release, calls: &calls}
			run := func(c context.Context, msg any) error {
				if stream {
					return inv.Stream(c, "work", msg, func(json.RawMessage) error { return nil })
				}
				return inv.Call(c, "work", msg, nil)
			}
			done := make(chan error, 1)
			go func() { done <- run(callCtx, msg) }()
			receiveResult(t, ctx, entered)
			requireBackpressure(t, run(ctx, msg))
			if calls.Load() != 1 || a.snapshot().Business.Pending != 1 {
				t.Fatal("admission after serialization")
			}
			cancelCall()
			a.closePending()
			if err := run(ctx, msg); !errors.Is(err, gordis.ErrClosed) {
				t.Fatal(err)
			}
			a.close()
			drained := make(chan error, 1)
			go func() { a.wait(); drained <- nil }()
			assertNotCompleted(t, drained)
			if a.snapshot().Business.Pending != 1 {
				t.Fatal("cancellation lost serialization")
			}
			unblock()
			if !errors.Is(receiveResult(t, ctx, done), context.Canceled) {
				t.Fatal("did not cancel before send")
			}
			requireNoError(t, receiveResult(t, ctx, drained))
			if a.snapshot() != (AdmissionSnapshot{}) {
				t.Fatal(a.snapshot())
			}
		})
	}
	// Encoding errors and pre-canceled calls also release/avoid the reservation.
	a := newAdmission(singleCallLimits())
	inv := &contractInvoker{admission: a}
	if err := inv.Call(context.Background(), "work", make(chan int), nil); err == nil {
		t.Fatal("accepted invalid JSON")
	}
	if err := inv.Stream(context.Background(), "work", make(chan int), nil); err == nil {
		t.Fatal("accepted invalid JSON")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := inv.Call(canceled, "work", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if a.snapshot() != (AdmissionSnapshot{}) {
		t.Fatal(a.snapshot())
	}
}
func TestAdmissionDeclarationAndLimits(t *testing.T) {
	defaults := Limits{}.defaults()
	if defaults.Business != (AdmissionLimit{}) || defaults.Control != (AdmissionLimit{}) || defaults.Continuation != (AdmissionLimit{}) {
		t.Fatal(defaults)
	}
	configured := Limits{
		Business:     AdmissionLimit{Pending: 3, Handling: 2},
		Control:      AdmissionLimit{Pending: 4, Handling: 5},
		Continuation: AdmissionLimit{Pending: 6, Handling: 7},
	}
	configured.Business.Pending = 0
	configured.Control.Handling = 0
	if got := configured.defaults(); got.Business != (AdmissionLimit{0, 2}) || got.Control != (AdmissionLimit{4, 0}) || got.Continuation != (AdmissionLimit{6, 7}) {
		t.Fatal("defaults replaced explicit limits", got)
	}
	for class := Business; class <= Continuation; class++ {
		for _, pending := range []bool{false, true} {
			limits := Limits{}
			groups := []*AdmissionLimit{&limits.Business, &limits.Control, &limits.Continuation}
			if pending {
				groups[class].Pending = -1
			} else {
				groups[class].Handling = -1
			}
			if _, err := NewProcess(ProcessOptions{PluginID: "p", Command: Command{Path: "must-not-start"}, Limits: limits}); err == nil {
				t.Fatal("accepted negative limit")
			}
			if _, err := DescribeProgram(context.Background(), Command{Path: "must-not-start"}, limits); err == nil {
				t.Fatal("accepted negative limit")
			}
			if err := ServeUnit(context.Background(), ServeUnitOptions{Identity: Identity{"p", "1", Protocol}, Limits: limits}); err == nil {
				t.Fatal("accepted negative limit")
			}
		}
	}
	classes := map[string]AdmissionClass{"work": Control}
	codec := Codec[*string]{MethodClasses: classes, Client: func(Invoker) *string { return new(string) }, Server: func(*string) Handler {
		return Handler{Methods: map[string]Unary{"work": func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}}
	}}
	binding := NewBinding(gordis.NewKey[*string]("test/admission"), "1", codec)
	classes["work"] = Continuation
	classes["bad"] = 255
	if binding.classes["work"] != Control {
		t.Fatal("mutable class map")
	}
	requireNoError(t, binding.valid())
	_, err := binding.local(context.Background(), new(string))
	requireNoError(t, err)
	for _, entry := range []struct {
		method string
		class  AdmissionClass
	}{{"", Business}, {"work", 255}} {
		codec.MethodClasses = map[string]AdmissionClass{entry.method: entry.class}
		if NewBinding(gordis.NewKey[*string]("test/admission"), "1", codec).valid() == nil {
			t.Fatal("accepted bad class")
		}
	}
	codec.MethodClasses = map[string]AdmissionClass{"unknown": Control}
	binding = NewBinding(gordis.NewKey[*string]("test/admission"), "1", codec)
	if _, err := binding.local(context.Background(), new(string)); err == nil {
		t.Fatal("accepted missing method")
	}
	codec.MethodClasses = map[string]AdmissionClass{"stream": Control}
	codec.Server = func(*string) Handler {
		return Handler{Streams: map[string]Stream{"stream": func(context.Context, json.RawMessage, func(json.RawMessage) error) error { return nil }}}
	}
	binding = NewBinding(gordis.NewKey[*string]("test/admission"), "1", codec)
	if _, err := binding.local(context.Background(), new(string)); err == nil {
		t.Fatal("accepted non-Business stream")
	}
	requireBackpressure(t, contextError(status.Error(codes.ResourceExhausted, "message too large")))
	requireBackpressure(t, contextError(rpcError(fmt.Errorf("domain: %w", ErrBackpressure))))
}
