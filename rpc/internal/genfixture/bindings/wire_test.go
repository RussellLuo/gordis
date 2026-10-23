package bindings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/business"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
)

type localInvoker struct{ handler rpc.Handler }

func (i localInvoker) Call(ctx context.Context, method string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	response, err := i.handler.Methods[method](ctx, raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(response, out)
}
func (i localInvoker) Stream(ctx context.Context, method string, in any, yield func(json.RawMessage) error) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return i.handler.Streams[method](ctx, raw, yield)
}

func TestUnaryWireRoundTrip(t *testing.T) {
	c := &serviceClient{localInvoker{newServiceHandler(&business.Plugin{Prefix: "Welcome"})}}
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Empty(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Value(ctx); v != 42 || err != nil {
		t.Fatalf("value=%d, %v", v, err)
	}
	if v, err := c.Greet(ctx, "Qi", 2); v != "Welcome, Qi!Welcome, Qi!" || err != nil {
		t.Fatalf("greet=%q, %v", v, err)
	}
	want := dto.Value{Count: 3, Node: &dto.Node{Name: "one", Next: &dto.Node{Name: "two"}}, Labels: map[string][]string{"a": {"b"}}, Pair: [2]int{1, 2}, Bytes: []byte{1, 2}, NamedBytes: []dto.Byte{3, 4}, Payload: dto.Raw(`null`)}
	if v, err := c.Echo(ctx, want); !reflect.DeepEqual(v, want) || err != nil {
		t.Fatalf("echo=%+v, %v", v, err)
	}
	if v, err := c.Nullable(ctx, nil); v != nil || err != nil {
		t.Fatalf("nullable=%v, %v", v, err)
	}
	if err := c.Fail(ctx); err == nil || !strings.Contains(err.Error(), "fixture business failure") {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.Wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUnaryRequestRejection(t *testing.T) {
	h := newServiceHandler(&business.Plugin{})
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"Args":[]}`, `{"args":null}`, `{"args":{}}`, `{"args":[]}`, `{"args":["x",1,2]}`,
		`{"args":[null,1]}`, `{"args":["x",null]}`, `{"args":["x",1.2]}`, `{"args":["x",999999999999999999999999]}`,
		`{"args":["x",1],"extra":true}`, `{"args":["x",1],"args":["x",1]}`, `{"args":["x",1]} {}`, `{"args":["x",1]}garbage`, `{"args":[`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := h.Methods["Greet"](context.Background(), json.RawMessage(raw)); err == nil {
				t.Fatal("accepted malformed request")
			}
		})
	}
	for _, raw := range []string{
		`null`, `{"count":null}`, `{"count":1,"unknown":0}`, `{"count":1,"Count":2}`, `{"count":1,"count":2}`,
		`{"node":{"name":null}}`, `{"node":{"next":{"name":null}}}`, `{"labels":{"a":[null]}}`, `{"pair":[1]}`, `{"pair":[1,null]}`,
		`{"bytes":[1,2]}`, `{"bytes":"!"}`, `{"named_bytes":[1]}`,
	} {
		t.Run("DTO/"+raw, func(t *testing.T) {
			if _, err := h.Methods["Echo"](context.Background(), json.RawMessage(`{"args":[`+raw+`]}`)); err == nil {
				t.Fatal("accepted malformed DTO")
			}
		})
	}
	for _, raw := range []string{`{"args":[{}]}`, `{"args":[{"node":null,"labels":null,"bytes":null,"named_bytes":null}]}`} {
		if _, err := h.Methods["Echo"](context.Background(), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

type replyInvoker struct {
	localInvoker
	response string
}

func (i replyInvoker) Call(_ context.Context, _ string, _, out any) error {
	return json.Unmarshal([]byte(i.response), out)
}

func TestUnaryResponseRejection(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"Value":1}`, `{"value":null}`, `{"value":"1"}`, `{"value":1,"extra":0}`, `{"value":1,"value":2}`, `{"value":1} {}`} {
		t.Run(raw, func(t *testing.T) {
			c := &serviceClient{replyInvoker{response: raw}}
			if _, err := c.Value(context.Background()); err == nil {
				t.Fatal("accepted malformed response")
			}
		})
	}
	for _, raw := range []string{`null`, `[]`, `{"value":null}`, `{"extra":0}`, `{} {}`} {
		t.Run("no-result/"+raw, func(t *testing.T) {
			c := &serviceClient{replyInvoker{response: raw}}
			if err := c.Ping(context.Background()); err == nil {
				t.Fatal("accepted malformed empty response")
			}
		})
	}
	if Service.Version() != "rpcgen.json/1:1" {
		t.Fatal(Service.Version())
	}
}
