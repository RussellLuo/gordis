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

func apply(t *testing.T, h *gordis.Host, c gordis.Change) uint64 {
	t.Helper()
	p, err := h.Preview(c)
	must(t, err)
	id, err := h.Apply(deadline(t), p)
	must(t, err)
	return id
}

func dynamicDefs(scopes chan *gordis.Scope) []gordis.Plugin {
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		if scopes != nil {
			scopes <- s
		}
		v := int(s.Generation())
		return gordis.Provide(s, valueKey, &v)
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	c := definition("consumer", func(_ context.Context, s *gordis.Scope) error {
		_, err := gordis.Get(s, valueKey)
		return err
	})
	c.Requires = []gordis.ServiceSpec{valueKey.Spec()}
	return []gordis.Plugin{p, c, definition("plain", func(context.Context, *gordis.Scope) error { return nil })}
}

func TestDynamicPendingIdentityAndIndependentSlots(t *testing.T) {
	scopes := make(chan *gordis.Scope, 8)
	h, err := gordis.NewHost(dynamicDefs(scopes), nil)
	must(t, err)
	c := gordis.InstanceSpec{
		ID:           "c",
		Plugin:       "consumer",
		Inputs:       map[string]string{valueKey.Name(): "alpha"},
		AllowPending: true,
	}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{c}})
	s := find(h, "c")
	if s.State != gordis.Pending ||
		s.Bindings[0].Provider != "" ||
		s.Bindings[0].Generation != 0 ||
		s.BlockedSlots[0] != "alpha" {
		t.Fatal(s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if !errors.Is(h.WaitReady(ctx, "c"), context.DeadlineExceeded) {
		t.Fatal("pending wait should time out")
	}
	alpha := gordis.InstanceSpec{ID: "alpha", Plugin: "provider", Outputs: map[string]string{valueKey.Name(): "alpha"}}
	beta := gordis.InstanceSpec{ID: "beta", Plugin: "provider", Outputs: map[string]string{valueKey.Name(): "beta"}}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{alpha, beta}})
	old := recv(t, scopes)
	_ = recv(t, scopes)
	if find(h, "c").Bindings[0].Generation != 1 {
		t.Fatal(h.Snapshot())
	}
	if _, err := h.Preview(gordis.Change{Remove: []string{"alpha"}}); err == nil {
		t.Fatal("implicit cascade accepted")
	}
	apply(t, h, gordis.Change{Remove: []string{"alpha"}, AllowWaitingConsumers: true})
	if find(h, "c").State != gordis.Pending || find(h, "beta").Generation != 1 {
		t.Fatal(h.Snapshot())
	}
	if _, err := old.Acquire(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{alpha}})
	if find(h, "alpha").Generation != 2 || find(h, "c").Generation != 2 || find(h, "beta").Generation != 1 {
		t.Fatal(h.Snapshot())
	}
	c.Disabled = true
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{c}})
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{alpha}})
	if find(h, "c").State == gordis.Ready || find(h, "c").DesiredEnabled {
		t.Fatal("disabled consumer revived")
	}
	must(t, h.Stop(deadline(t)))
}

func TestDynamicValidationAndStalePlanDoNotFreeze(t *testing.T) {
	scopes := make(chan *gordis.Scope, 2)
	defs := dynamicDefs(scopes)
	h, err := gordis.NewHost(defs, nil)
	must(t, err)
	p := gordis.InstanceSpec{ID: "p", Plugin: "provider"}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{p}})
	s := recv(t, scopes)
	for _, change := range []gordis.Change{
		{Upsert: []gordis.InstanceSpec{{ID: "q", Plugin: "provider"}}},
		{Upsert: []gordis.InstanceSpec{{ID: "x", Plugin: "plain", Parent: "missing", AllowPending: true}}},
		{Upsert: []gordis.InstanceSpec{{ID: "p", Plugin: "provider", Parent: "c"}, {ID: "c", Plugin: "consumer"}}},
		{Upsert: []gordis.InstanceSpec{{ID: "p", Plugin: "provider", Config: json.RawMessage(`{`)}}},
	} {
		if _, err := h.Preview(change); err == nil {
			t.Fatal("bad candidate accepted")
		}
		release, err := s.Acquire()
		must(t, err)
		release()
	}
	plan, err := h.Preview(gordis.Change{Upsert: []gordis.InstanceSpec{p}})
	must(t, err)
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{{ID: "x", Plugin: "plain"}}})
	if _, err := h.Apply(deadline(t), plan); !errors.Is(err, gordis.ErrStale) {
		t.Fatal(err)
	}
	if find(h, "p").Generation != 1 {
		t.Fatal(h.Snapshot())
	}
	must(t, h.Stop(deadline(t)))
}

func TestDynamicDrainTimeoutAndResidualPin(t *testing.T) {
	for _, residual := range []bool{false, true} {
		t.Run(map[bool]string{false: "lease", true: "residual"}[residual], func(t *testing.T) {
			scopes := make(chan *gordis.Scope, 2)
			d := definition("d", func(_ context.Context, s *gordis.Scope) error {
				scopes <- s
				if residual {
					return s.Defer("broken", func(context.Context) error { return errors.New("still held") })
				}
				return nil
			})
			h, err := gordis.NewHost([]gordis.Plugin{d}, nil)
			must(t, err)
			spec := gordis.InstanceSpec{ID: "x", Plugin: "d"}
			apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{spec}})
			s := recv(t, scopes)
			release, err := s.Acquire()
			must(t, err)
			p, err := h.Preview(gordis.Change{Upsert: []gordis.InstanceSpec{spec}})
			must(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			id, err := h.Apply(ctx, p)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			o, _ := h.Operation(id)
			if o.Phase != "draining" || o.Committed || find(h, "x").Generation != 1 {
				t.Fatal(o, h.Snapshot())
			}
			if _, err := s.Acquire(); !errors.Is(err, gordis.ErrClosed) {
				t.Fatal(err)
			}
			release()
			err = h.WaitOperation(deadline(t), id)
			if residual {
				if err == nil || find(h, "x").Generation != 1 {
					t.Fatal(err, h.Snapshot())
				}
				o, _ = h.Operation(id)
				if o.Committed || o.Phase != "failed" {
					t.Fatal(o)
				}
				if _, err = h.Restore(deadline(t), id); err == nil {
					t.Fatal("residual overwritten")
				}
			} else {
				must(t, err)
				if find(h, "x").Generation != 2 {
					t.Fatal(h.Snapshot())
				}
				must(t, h.Stop(deadline(t)))
			}
		})
	}
}

func TestDynamicOptionalMetricsAndOwnership(t *testing.T) {
	h, err := gordis.NewHost(dynamicDefs(nil), nil)
	must(t, err)
	parent := gordis.InstanceSpec{ID: "parent", Plugin: "plain"}
	metrics := gordis.InstanceSpec{ID: "metrics", Plugin: "consumer", Parent: "parent", Optional: true, AllowPending: true}
	provider := gordis.InstanceSpec{ID: "p", Plugin: "provider"}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{parent, metrics}})
	if !find(h, "parent").GroupReady || find(h, "metrics").State != gordis.Pending {
		t.Fatal(h.Snapshot())
	}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{provider}})
	apply(t, h, gordis.Change{Remove: []string{"p"}, AllowWaitingConsumers: true})
	if find(h, "parent").Generation != 1 || !find(h, "parent").GroupReady || find(h, "metrics").State != gordis.Pending {
		t.Fatal(h.Snapshot())
	}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{provider}})
	apply(t, h, gordis.Change{Remove: []string{"parent"}})
	if len(h.Snapshot()) != 1 || find(h, "p").State != gordis.Ready {
		t.Fatal(h.Snapshot())
	}
	must(t, h.Stop(deadline(t)))
}

func TestDynamicRebindingUsesOldCleanupAndNewStartup(t *testing.T) {
	defs := dynamicDefs(nil)
	h, err := gordis.NewHost(defs, nil)
	must(t, err)
	a := gordis.InstanceSpec{ID: "a", Plugin: "provider", Outputs: map[string]string{valueKey.Name(): "a"}}
	b := gordis.InstanceSpec{ID: "b", Plugin: "provider", Outputs: map[string]string{valueKey.Name(): "b"}}
	c := gordis.InstanceSpec{
		ID:           "c",
		Plugin:       "consumer",
		Inputs:       map[string]string{valueKey.Name(): "a"},
		AllowPending: true,
	}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{a, b, c}})
	c.Inputs[valueKey.Name()] = "b"
	apply(t, h, gordis.Change{Remove: []string{"a"}, Upsert: []gordis.InstanceSpec{c}, AllowWaitingConsumers: true})
	s := find(h, "c")
	if s.Generation != 2 || s.Bindings[0].Provider != "b" || find(h, "b").Generation != 1 {
		t.Fatal(h.Snapshot())
	}
	must(t, h.Stop(deadline(t)))
}

func TestDynamicUpgradeAndExplicitRecoveryFailures(t *testing.T) {
	var rejectOld atomic.Bool
	d := &versionPlugin{rejectOld: &rejectOld}
	h, err := gordis.NewHost([]gordis.Plugin{d}, nil)
	must(t, err)
	s := gordis.InstanceSpec{ID: "x", Plugin: "d", Version: "old", Config: json.RawMessage(`{"Version":"old"}`)}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{s}})
	s.Config = json.RawMessage(`{"Version":"bad"}`)
	s.Version = "bad"
	p, err := h.Preview(gordis.Change{Upsert: []gordis.InstanceSpec{s}})
	must(t, err)
	id, err := h.Apply(deadline(t), p)
	if err == nil || find(h, "x").State != gordis.Failed {
		t.Fatal(err, h.Snapshot())
	}
	recovery, err := h.Restore(deadline(t), id)
	must(t, err)
	if find(h, "x").Generation != 3 || find(h, "x").Version != "old" {
		t.Fatal(h.Snapshot())
	}
	o, _ := h.Operation(recovery)
	if o.Restores != id {
		t.Fatal(o)
	}
	p, err = h.Preview(gordis.Change{Upsert: []gordis.InstanceSpec{s}})
	must(t, err)
	id, err = h.Apply(deadline(t), p)
	if err == nil {
		t.Fatal("upgrade succeeded")
	}
	rejectOld.Store(true)
	recovery, err = h.Restore(deadline(t), id)
	if err == nil || !strings.Contains(err.Error(), "restore rejected") {
		t.Fatal(err)
	}
	o, _ = h.Operation(id)
	if !strings.Contains(o.Error, "upgrade rejected") {
		t.Fatal(o)
	}
	o, _ = h.Operation(recovery)
	if o.Phase != "failed" || !strings.Contains(o.Error, "restore rejected") {
		t.Fatal(o)
	}
	if find(h, "x").State != gordis.Failed || find(h, "x").Generation != 5 {
		t.Fatal(h.Snapshot())
	}
	must(t, h.Stop(deadline(t)))
}

func TestDynamicRuntimeFailureWaitsForExplicitRecovery(t *testing.T) {
	fail := make(chan error, 1)
	defs := dynamicDefs(nil)
	provider := defs[0].(*testPlugin)
	original := provider.StartFunc
	provider.StartFunc = func(ctx context.Context, s *gordis.Scope) error {
		if err := original(ctx, s); err != nil {
			return err
		}
		return s.Go("fail", func(ctx context.Context) error {
			select {
			case err := <-fail:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	h, err := gordis.NewHost(defs, nil)
	must(t, err)
	p := gordis.InstanceSpec{ID: "p", Plugin: "provider"}
	c := gordis.InstanceSpec{ID: "c", Plugin: "consumer", AllowPending: true}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{p, c}})
	fail <- errors.New("crashed")
	for end := time.Now().Add(time.Second); ; {
		if find(h, "c").State == gordis.Pending {
			break
		}
		if time.Now().After(end) {
			t.Fatal(h.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	if find(h, "p").State != gordis.Failed || !strings.Contains(find(h, "p").LastError, "crashed") {
		t.Fatal(h.Snapshot())
	}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{{ID: "unrelated", Plugin: "plain"}}})
	if find(h, "p").Generation != 1 {
		t.Fatal("fault automatically retried")
	}
	// The legacy explicit start API also reconciles opted-in pending consumers.
	must(t, h.StartInstance(deadline(t), "p"))
	if find(h, "p").Generation != 2 || find(h, "c").Bindings[0].Generation != 2 {
		t.Fatal(h.Snapshot())
	}
	must(t, h.Stop(deadline(t)))
}

func TestDynamicRevalidatesPreflightAndKeepsNewHostStrict(t *testing.T) {
	defs := dynamicDefs(nil)
	pending := gordis.InstanceSpec{ID: "c", Plugin: "consumer", AllowPending: true}
	if _, err := gordis.NewHost(defs, []gordis.InstanceSpec{pending}); err == nil {
		t.Fatal("NewHost silently allowed missing dependency")
	}
	var rejected atomic.Bool
	defs[2].(*testPlugin).ValidateFunc = func() error {
		if rejected.Load() {
			return errors.New("package changed since preview")
		}
		return nil
	}
	h, err := gordis.NewHost(defs, nil)
	must(t, err)
	spec := gordis.InstanceSpec{ID: "x", Plugin: "plain"}
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{spec}})
	p, err := h.Preview(gordis.Change{Upsert: []gordis.InstanceSpec{spec}})
	must(t, err)
	rejected.Store(true)
	if id, err := h.Apply(deadline(t), p); err == nil || id != 0 {
		t.Fatal(id, err)
	}
	if find(h, "x").State != gordis.Ready || find(h, "x").Generation != 1 {
		t.Fatal(h.Snapshot())
	}
	must(t, h.Stop(deadline(t)))
}

type versionPlugin struct {
	Version   string `json:"Version"`
	rejectOld *atomic.Bool
}

func (p *versionPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "d", New: func() gordis.Plugin { return &versionPlugin{rejectOld: p.rejectOld} }}
}

func (p *versionPlugin) Start(context.Context, *gordis.Scope) error {
	if p.Version == "bad" {
		return errors.New("upgrade rejected")
	}
	if p.rejectOld.Load() {
		return errors.New("restore rejected")
	}
	return nil
}

func TestDynamicParentRemovalWaitsForChildLease(t *testing.T) {
	scopes := make(chan *gordis.Scope, 2)
	d := definition("plain", func(_ context.Context, s *gordis.Scope) error { scopes <- s; return nil })
	h, err := gordis.NewHost([]gordis.Plugin{d}, nil)
	must(t, err)
	apply(t, h, gordis.Change{Upsert: []gordis.InstanceSpec{
		{ID: "parent", Plugin: "plain"},
		{ID: "child", Plugin: "plain", Parent: "parent", Optional: true},
	}})
	parent, child := recv(t, scopes), recv(t, scopes)
	release, err := child.Acquire()
	must(t, err)
	p, err := h.Preview(gordis.Change{Remove: []string{"parent"}})
	must(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	id, err := h.Apply(ctx, p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if find(h, "parent").CleanupComplete || find(h, "child").CleanupComplete {
		t.Fatal(h.Snapshot())
	}
	if _, err := parent.Acquire(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	release()
	must(t, h.WaitOperation(deadline(t), id))
	if len(h.Snapshot()) != 0 {
		t.Fatal(h.Snapshot())
	}
}
