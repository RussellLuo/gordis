package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

func TestBridgeRegistryAndCapabilities(t *testing.T) {
	good := BindTopic(notice, JSONEventCodec[string]("string/1"), Publish|Subscribe)
	for _, bindings := range [][]Binding{nil, {{}}, {good, good}, {BindTopic(notice, JSONEventCodec[string](""), Publish)}, {BindTopic(notice, EventCodec[string]{ID: "bad"}, Publish)}, {BindTopic(notice, JSONEventCodec[string]("s"), 0)}, {BindTopic(notice, JSONEventCodec[string]("s"), 8)}, {BindTopic(core.Topic[string]{}, JSONEventCodec[string]("s"), Publish)}, {good, BindTopic(core.NewTopic[int](notice.ID()), JSONEventCodec[int]("i"), Publish)}} {
		if _, err := NewBridge(bindings...); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid registry accepted: %v", err)
		}
	}
	a, _ := NewBridge(good, BindTopic(other, JSONEventCodec[string]("string/1"), Publish))
	b, _ := NewBridge(BindTopic(other, JSONEventCodec[string]("string/1"), Publish), good)
	if a.host.Version() != b.host.Version() || a.child.Version() != b.child.Version() {
		t.Fatal("order-dependent digest")
	}
	if _, err := a.local(core.NewTopic[int](notice.ID()), Publish); !errors.Is(err, ErrCodec) {
		t.Fatal("same ID wrong Go type", err)
	}
	if _, err := a.local(core.NewHook[string, string](notice.ID()), Publish); !errors.Is(err, ErrCodec) {
		t.Fatal("same ID wrong kind", err)
	}
	if _, err := a.lookup(other.ID(), Subscribe); !errors.Is(err, ErrDirection) {
		t.Fatal(err)
	}
	// This is a real handshake: the Child has a different registry, and never
	// registers an owner or calls any business callback before rejection.
	h := newHarness(t, processSetup{"a", a, nil})
	_, err := h.host.Env().MountReady(h.ctx, gordis.InstanceSpec{ID: "a", Plugin: "a"})
	if err == nil {
		t.Fatal("mismatched Describe accepted")
	}
	p := <-h.opened["a"]
	if p.EventsSnapshot().Owners != 0 || len(h.messages) != 0 || !p.Snapshot().Reaped {
		t.Fatal("registry mismatch initialized business", p.EventsSnapshot(), p.Snapshot())
	}
	// Bus, reserved IDs, duplicate declarations and unclosed dependencies fail
	// while assembling the Unit; no child or plugin activation is performed.
	bridge := testBridge(t, false)
	missing := gordis.NewKey[string]("missing")
	badUnits := []rpc.Unit{
		{Plugins: []gordis.Plugin{nil}},
		{Plugins: []gordis.Plugin{&testPlugin{spec: gordis.PluginSpec{ID: "duplicate"}}, &testPlugin{spec: gordis.PluginSpec{ID: "duplicate"}}}},
		{Plugins: []gordis.Plugin{&core.Plugin{}}},
		{Plugins: []gordis.Plugin{&testPlugin{spec: gordis.PluginSpec{ID: adapterPluginID}}}},
		{Instances: []gordis.InstanceSpec{{ID: adapterInstanceID}}},
		{Imports: []rpc.Binding{bridge.host}},
		{Plugins: []gordis.Plugin{&testPlugin{spec: gordis.PluginSpec{ID: "x", Requires: []gordis.ServiceSpec{missing.Spec()}}}}, Instances: []gordis.InstanceSpec{{ID: "x", Plugin: "x"}}},
		{Imports: []rpc.Binding{testBinding(testHostKey), testBinding(testHostKey)}},
	}
	for _, unit := range badUnits {
		if _, err := bridge.Unit(unit); err == nil {
			t.Fatal("invalid Unit accepted")
		}
	}
	unit := rpc.Unit{Plugins: []gordis.Plugin{&testPlugin{spec: gordis.PluginSpec{ID: "x"}}}, Instances: []gordis.InstanceSpec{{ID: "x", Plugin: "x", Config: json.RawMessage(`{"preset":1}`)}}}
	out, err := bridge.Unit(unit)
	if err != nil {
		t.Fatal(err)
	}
	out.Instances[0].Config[0] = '!'
	if unit.Instances[0].Config[0] != '{' || len(unit.Plugins) != 1 || len(unit.Instances) != 1 {
		t.Fatal("Unit input mutated")
	}
}

func TestBridgeStrictWireAndCodecs(t *testing.T) {
	codec := JSONEventCodec[struct {
		Value int `json:"value"`
	}]("v/1")
	for _, raw := range []string{`null`, `{"value":1,"extra":2}`, `{"value":1,"value":2}`, `{"value":"x"}`, `{"value":1} false`, `{`} {
		if _, err := codec.Decode(json.RawMessage(raw)); !errors.Is(err, ErrCodec) {
			t.Fatal("payload accepted", raw, err)
		}
	}
	if _, err := codec.Decode(json.RawMessage(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	nullable := JSONEventCodec[*int]("ptr/1")
	if msg, err := nullable.Decode(json.RawMessage(`null`)); msg != nil || err != nil {
		t.Fatal(msg, err)
	}
	bad := BindTopic(notice, EventCodec[string]{ID: "bad", Encode: func(string) (json.RawMessage, error) { panic("encode") }, Decode: func(json.RawMessage) (string, error) { panic("decode") }}, Publish|Subscribe)
	if _, err := bad.encode("msg"); !errors.Is(err, ErrCodec) {
		t.Fatal("encode panic", err)
	}
	if _, err := bad.decode(json.RawMessage(`"msg"`)); !errors.Is(err, ErrCodec) {
		t.Fatal("decode panic", err)
	}
	invalid := BindTopic(notice, EventCodec[string]{ID: "bad", Encode: func(string) (json.RawMessage, error) { return json.RawMessage(`{} {}`), nil }, Decode: JSONEventCodec[string]("s").Decode}, Publish)
	if _, err := invalid.encode("msg"); !errors.Is(err, ErrCodec) {
		t.Fatal("invalid custom JSON", err)
	}
	ptr := BindTopic(core.NewTopic[*int]("ptr"), JSONEventCodec[*int]("ptr/1"), Publish)
	if raw, err := ptr.encode((*int)(nil)); err != nil || string(raw) != "null" {
		t.Fatal("legal nullable payload rejected", string(raw), err)
	}
	h := newHarness(t)
	p, _, _ := h.process("a")
	peer := p.peer
	invalidRequests := []struct{ method, raw string }{
		{"RegisterOwner", `{}`}, {"RegisterOwner", `{"owner":null}`}, {"RegisterOwner", `{"owner":{"Instance":"x"}}`}, {"RegisterOwner", `{"owner":{"Instance":"x","Generation":1},"extra":0}`},
		{"Subscribe", `{"owner":{"Instance":"x","Generation":1},"local_id":1,"contract":"ev02/notice","kind":2,"options":{"Once":false,"Global":false,"Priority":0}}`},
		{"Dispatch", `{"owner":{"Instance":"owner","Generation":1},"contract":"ev02/notice","mode":1,"payload":"ok","source":{"name":"forged"}}`},
		{"Dispatch", `{"owner":{"Instance":"owner","Generation":1},"contract":"ev02/notice","mode":3,"payload":"ok"}`},
		{"CommitSubscription", `{"id":1,"id":2}`}, {"Unsubscribe", `{"id":null}`}, {"Unsubscribe", `{"id":1} {}`},
	}
	for _, test := range invalidRequests {
		if _, err := peer.invoke(h.ctx, test.method, json.RawMessage(test.raw)); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(test, err)
		}
	}
	for _, err := range []error{ErrInvalidRequest, ErrDirection, ErrUndeclaredContract, ErrCodec, gordis.ErrClosed, gordis.ErrStale, gordis.ErrNotReady, context.Canceled, context.DeadlineExceeded, rpc.ErrBackpressure, core.ErrNoHandler} {
		if !errors.Is(errorReply(err).err(), err) {
			t.Fatal("error roundtrip", err)
		}
	}
	merged := errors.Join(gordis.ErrStale, context.Canceled, rpc.ErrBackpressure)
	restored := errorReply(merged).err()
	if !errors.Is(restored, gordis.ErrStale) || !errors.Is(restored, context.Canceled) || !errors.Is(restored, rpc.ErrBackpressure) {
		t.Fatal(restored)
	}
}

func TestBridgeSubscriptionCapacityAndOwnerTombstones(t *testing.T) {
	h := newHarness(t)
	peer := newHostPeer(testBridge(t, false), h.scope)
	t.Cleanup(peer.close)
	ref := core.OwnerRef{Instance: "fixture", Generation: 7}
	if _, err := call(h.ctx, peer, "RegisterOwner", ownerRequest{ref}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxSubscriptions; i++ {
		out, err := call(h.ctx, peer, "Subscribe", subscribeRequest{ref, uint64(i), notice.ID(), core.RemoteNotification, core.RemoteOptions{}})
		if err != nil || out.ID != uint64(i) {
			t.Fatal(i, out, err)
		}
	}
	if _, err := call(h.ctx, peer, "Subscribe", subscribeRequest{ref, maxSubscriptions + 1, notice.ID(), core.RemoteNotification, core.RemoteOptions{}}); !errors.Is(err, rpc.ErrBackpressure) {
		t.Fatal("subscription limit", err)
	}
	if peer.snapshot().Pending != maxSubscriptions {
		t.Fatal(peer.snapshot())
	}
	for i := 1; i <= maxSubscriptions/2; i++ {
		if _, err := call(h.ctx, peer, "CommitSubscription", idRequest{uint64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if d := peer.snapshot(); d.Pending != maxSubscriptions/2 || d.Committed != maxSubscriptions/2 {
		t.Fatal("committed capacity", d)
	}
	if _, err := call(h.ctx, peer, "OwnerReady", ownerRequest{ref}); err != nil {
		t.Fatal(err)
	}
	if d := peer.snapshot(); d.Active != maxSubscriptions/2 || d.Pending != maxSubscriptions/2 {
		t.Fatal("active capacity", d)
	}
	if _, err := call(h.ctx, peer, "Subscribe", subscribeRequest{ref, maxSubscriptions + 1, notice.ID(), core.RemoteNotification, core.RemoteOptions{}}); !errors.Is(err, rpc.ErrBackpressure) {
		t.Fatal("active/committed escaped capacity", err)
	}
	if _, err := call(h.ctx, peer, "Unsubscribe", idRequest{1}); err != nil {
		t.Fatal(err)
	}
	out, err := call(h.ctx, peer, "Subscribe", subscribeRequest{ref, maxSubscriptions + 2, notice.ID(), core.RemoteNotification, core.RemoteOptions{}})
	if err != nil || out.ID != maxSubscriptions+1 {
		t.Fatal("capacity not reclaimed or ID reused", out, err)
	}
	if _, err := call(h.ctx, peer, "CommitSubscription", idRequest{1}); !errors.Is(err, gordis.ErrStale) {
		t.Fatal("removed Commit revived", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := call(h.ctx, peer, "RetireOwner", ownerRequest{ref}); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"RegisterOwner", "OwnerReady"} {
		if _, err := call(h.ctx, peer, method, ownerRequest{ref}); !errors.Is(err, gordis.ErrClosed) {
			t.Fatal("owner revived", method, err)
		}
	}
	// Retirement may win before first registration.
	late := core.OwnerRef{Instance: "late", Generation: 8}
	_, _ = call(h.ctx, peer, "RetireOwner", ownerRequest{late})
	if _, err := call(h.ctx, peer, "RegisterOwner", ownerRequest{late}); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if peer.snapshot().Pending+peer.snapshot().Active+peer.snapshot().Committed != 0 {
		t.Fatal("retired owner leaked subscriptions")
	}
}

func TestBridgeStrictReplies(t *testing.T) {
	for _, test := range []struct{ method, raw string }{
		{"Subscribe", `{"id":0,"error":null}`}, {"Enable", `{"id":1,"error":null}`}, {"Enable", `{"id":0}`}, {"Enable", `{"id":0,"error":{"codes":["unknown"],"message":"bad"}}`}, {"Enable", `{"id":0,"error":null,"extra":false}`}, {"Enable", `{"id":0,"id":0,"error":null}`},
	} {
		value := decodedReply{method: test.method}
		if err := json.Unmarshal([]byte(test.raw), &value); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(test, err)
		}
	}
	result := decodedReply{method: "Enable"}
	if err := json.Unmarshal([]byte(`{"id":0,"error":null}`), &result); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeHookRegistryAndWire(t *testing.T) {
	codec := JSONEventCodec[string]("string/1")
	hook := BindHook(transform, codec, codec, Publish|Subscribe)
	for _, bad := range []Binding{BindHook(transform, codec, EventCodec[string]{ID: "out/1"}, Publish), BindHook(transform, codec, JSONEventCodec[string](""), Publish), BindHook(core.Hook[string, string]{}, codec, codec, Publish)} {
		if _, err := NewBridge(bad); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
	}
	a, _ := NewBridge(hook)
	other, _ := NewBridge(BindHook(transform, codec, JSONEventCodec[string]("string/2"), Publish|Subscribe))
	if a.host.Version() == other.host.Version() {
		t.Fatal("output codec omitted from digest")
	}
	if _, err := a.local(core.NewHook[string, int](transform.ID()), Publish); !errors.Is(err, ErrCodec) {
		t.Fatal(err)
	}
	if _, err := NewBridge(hook, BindTopic(core.NewTopic[string](transform.ID()), codec, Publish)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal(err)
	}
	h := newHarness(t, hookSetup(t, "a", false))
	p, _, endpoint := h.process("a")
	if _, err := hookCall(h.ctx, endpoint, "Serial", "panic", hook); err == nil {
		t.Fatal("handler panic reported success")
	}
	if _, err := hookCall(h.ctx, endpoint, "Waterfall", "base-panic", p.peer.bridge.bindings[transform.ID()]); err == nil {
		t.Fatal("base panic reported success")
	}
	// Wrong modes/kinds, missing base, forged optional fields and output shapes.
	for _, raw := range []string{
		`{"owner":{"Instance":"owner","Generation":1},"contract":"ev03/transform","mode":1,"payload":"ok"}`,
		`{"owner":{"Instance":"owner","Generation":1},"contract":"ev03/transform","mode":4,"payload":"ok"}`,
		`{"owner":{"Instance":"owner","Generation":1},"contract":"ev03/transform","mode":3,"payload":"ok","token":1}`,
		`{"owner":{"Instance":"owner","Generation":1},"contract":"ev03/transform","mode":3,"payload":"ok","source":null}`,
	} {
		if _, err := p.peer.invoke(h.ctx, "Dispatch", json.RawMessage(raw)); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{
		`{"id":0,"error":null}`,
		`{"id":0,"error":null,"result":{"payload":123,"handled":true}}`,
		`{"id":0,"error":null,"result":{"payload":null,"handled":true}}`,
		`{"id":0,"error":null,"result":{"payload":"ok"}}`,
		`{"id":0,"error":null,"result":{"payload":"ok","handled":true,"extra":1}}`,
		`{"id":0,"error":null,"result":{"payload":"ok","handled":false}}`,
	} {
		out := decodedReply{method: "Continue", binding: &hook, hook: true, requireHandled: true}
		if err := json.Unmarshal([]byte(raw), &out); err == nil {
			t.Fatal("bad hook result", raw)
		}
	}
	nullable := BindHook(core.NewHook[string, *int]("ev03/nullable"), codec, JSONEventCodec[*int]("pointer/1"), Publish)
	out := decodedReply{method: "Continue", binding: &nullable, hook: true, requireHandled: true}
	if err := json.Unmarshal([]byte(`{"id":0,"error":null,"result":{"payload":null,"handled":true}}`), &out); err != nil || out.value.value.Value != (*int)(nil) {
		t.Fatal(err, out.value)
	}
}
