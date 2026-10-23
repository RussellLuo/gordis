package bindings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture/contract"
)

type generator struct{ calls int }

func (g *generator) Run(ctx context.Context, _ string, count int, yield func(dto.Value) error) error {
	g.calls++
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(dto.Value{Count: i}); err != nil {
			return err
		}
	}
	return nil
}
func (g *generator) Nullable(_ context.Context, yield contract.Yield) error {
	g.calls++
	if err := yield(nil); err != nil {
		return err
	}
	return yield(&dto.Node{Name: "last"})
}

type streamInvoker struct {
	handler  rpc.Handler
	raw      *string
	ctx      context.Context
	received int
}

func (*streamInvoker) Call(context.Context, string, any, any) error {
	return errors.New("unexpected unary")
}
func (i *streamInvoker) Stream(ctx context.Context, method string, in any, yield func(json.RawMessage) error) error {
	i.ctx = ctx
	if i.raw != nil {
		return yield(json.RawMessage(*i.raw))
	}
	request, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return i.handler.Streams[method](ctx, request, func(raw json.RawMessage) error { i.received++; return yield(raw) })
}
func TestStreamWireRoundTrip(t *testing.T) {
	g := &generator{}
	i := &streamInvoker{handler: newGeneratorHandler(g)}
	c := &generatorClient{i}
	var got []int
	if err := c.Run(context.Background(), "finite", 3, func(v dto.Value) error { got = append(got, v.Count); return nil }); err != nil || !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("%v %v", got, err)
	}
	var nullable []*dto.Node
	if err := c.Nullable(context.Background(), func(v *dto.Node) error { nullable = append(nullable, v); return nil }); err != nil || len(nullable) != 2 || nullable[0] != nil || nullable[1].Name != "last" {
		t.Fatalf("%v %v", nullable, err)
	}
	sentinel := errors.New("stop locally")
	i.received = 0
	if err := c.Run(context.Background(), "stop", 100, func(dto.Value) error { return sentinel }); err != sentinel || i.received != 1 || i.ctx.Err() != context.Canceled {
		t.Fatalf("sentinel/cancel: %v %d %v", err, i.received, i.ctx.Err())
	}
	called := g.calls
	if err := c.Nullable(context.Background(), nil); err == nil || g.calls != called {
		t.Fatal("nil yield reached server")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Run(cancelled, "cancel", 1, func(dto.Value) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestStreamRequestRejection(t *testing.T) {
	g := &generator{}
	h := newGeneratorHandler(g)
	for _, raw := range []string{`null`, `[]`, `{}`, `{"args":null}`, `{"args":[]}`, `{"args":["x"]}`, `{"args":["x",1,2]}`, `{"Args":["x",1]}`, `{"args":["x",1],"extra":1}`, `{"args":["x",1],"args":[]}`, `{"args":[null,1]}`, `{"args":["x",null]}`, `{"args":["x","1"]}`, `{"args":["x",1]} {}`, `{"args":[`} {
		t.Run(raw, func(t *testing.T) {
			if err := h.Streams["Run"](context.Background(), json.RawMessage(raw), func(json.RawMessage) error { t.Fatal("yield after bad request"); return nil }); err == nil {
				t.Fatal("accepted request")
			}
		})
	}
	if g.calls != 0 {
		t.Fatal("malformed request reached provider")
	}
}
func TestStreamChunkRejection(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"Value":{}}`, `{"value":null}`, `{"value":{},"extra":0}`, `{"value":{},"value":{}}`, `{"value":{}} {}`, `{"value":`, `{"value":{"count":null}}`, `{"value":{"unknown":1}}`, `{"value":{"count":1,"count":2}}`, `{"value":{"node":{"name":null}}}`, `{"value":{"labels":{"x":[null]}}}`, `{"value":{"pair":[1]}}`, `{"value":{"bytes":"!"}}`} {
		t.Run(raw, func(t *testing.T) {
			i := &streamInvoker{raw: &raw}
			c := &generatorClient{i}
			if err := c.Run(context.Background(), "bad", 1, func(dto.Value) error { t.Fatal("bad chunk reached yield"); return nil }); err == nil {
				t.Fatal("accepted chunk")
			}
			if i.ctx.Err() != context.Canceled {
				t.Fatal("bad chunk did not cancel stream")
			}
		})
	}
}
