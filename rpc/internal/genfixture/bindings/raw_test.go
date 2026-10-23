package bindings

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis/rpc/internal/genfixture/business"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
)

func TestRawJSONRoundTrip(t *testing.T) {
	client := &serviceClient{localInvoker{newServiceHandler(&business.Plugin{})}}
	ctx := context.Background()
	for _, raw := range []string{
		`null`, `true`, `"hello"`, `9007199254740993`,
		`[null,1,{"arbitrary":[true]}]`, `{"name":"Qi","name":"other","unknown":1}`,
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := client.EchoJSON(ctx, json.RawMessage(raw))
			if err != nil || string(got) != raw {
				t.Fatalf("raw value changed: %s, %v", got, err)
			}
			var chunks []string
			err = client.JSONStream(ctx, json.RawMessage(raw), func(v dto.Raw) error {
				chunks = append(chunks, string(v))
				return nil
			})
			if err != nil || !reflect.DeepEqual(chunks, []string{raw}) {
				t.Fatalf("raw chunk changed: %v, %v", chunks, err)
			}
		})
	}
	// Standard RawMessage marshaling sends nil as JSON null.
	if got, err := client.EchoJSON(ctx, nil); err != nil || string(got) != "null" {
		t.Fatalf("nil raw: %s, %v", got, err)
	}
	for _, raw := range []string{"", `{"broken":`, `{} {}`, `undefined`} {
		if _, err := client.EchoJSON(ctx, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid JSON %q", raw)
		}
	}

	optional := dto.Raw(`{"optional":true}`)
	want := dto.Value{
		Payload:   dto.Raw(`{"schema":{"type":"object","additionalProperties":true}}`),
		Optional:  &optional,
		Documents: []dto.Raw{dto.Raw(`123`), dto.Raw(`"text"`), dto.Raw(`{"x":[1,2]}`)},
		Bytes:     []byte{1, 2}, RawBytes: dto.RawBytes{3, 4},
	}
	if got, err := client.Echo(ctx, want); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("nested raw: %+v, %v", got, err)
	}
	// Raw JSON is embedded; distinct byte types keep the base64 wire format.
	h := newServiceHandler(&business.Plugin{})
	for _, raw := range []string{`{"raw_bytes":[1]}`, `{"raw_bytes":{"x":1}}`, `{"payload":{},"unexpected":1}`} {
		if _, err := h.Methods["Echo"](ctx, json.RawMessage(`{"args":[`+raw+`]}`)); err == nil {
			t.Fatalf("raw support relaxed DTO/byte checks: %s", raw)
		}
	}
	// Only the RawMessage subtree is opaque. Typed envelope fields stay strict.
	for _, raw := range []string{`{"args":[{}],"extra":1}`, `{"args":[{}],"args":[{}]}`, `{"Args":[{}]}`} {
		if _, err := h.Methods["EchoJSON"](ctx, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted bad raw envelope: %s", raw)
		}
	}
}

type rawStreamReply struct{ replyInvoker }

func (i rawStreamReply) Stream(_ context.Context, _ string, _ any, yield func(json.RawMessage) error) error {
	return yield(json.RawMessage(i.response))
}

func TestRawJSONResponseAndChunkBoundaries(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{`null`, `[]`, `{}`, `{"Value":1}`, `{"value":1,"extra":0}`, `{"value":1,"value":2}`, `{"value":1} {}`, `{"value":}`} {
		t.Run(raw, func(t *testing.T) {
			client := &serviceClient{rawStreamReply{replyInvoker{response: raw}}}
			if _, err := client.EchoJSON(ctx, json.RawMessage(`null`)); err == nil {
				t.Fatal("accepted invalid raw response envelope")
			}
			called := false
			if err := client.JSONStream(ctx, json.RawMessage(`null`), func(dto.Raw) error {
				called = true
				return nil
			}); err == nil || called {
				t.Fatal("accepted invalid raw chunk envelope")
			}
		})
	}
}
