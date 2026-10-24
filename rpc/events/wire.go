package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/RussellLuo/gordis"
	core "github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/rpc"
)

var (
	ErrInvalidRequest     = errors.New("rpc/events: invalid request")
	ErrUndeclaredContract = errors.New("rpc/events: undeclared contract")
	ErrDirection          = errors.New("rpc/events: direction denied")
	ErrInvalidSource      = errors.New("rpc/events: invalid source service")
	ErrCodec              = errors.New("rpc/events: incompatible codec or type")
)

var errorKinds = []struct {
	code string
	err  error
}{
	{"invalid_request", ErrInvalidRequest}, {"undeclared_contract", ErrUndeclaredContract}, {"direction", ErrDirection}, {"codec", ErrCodec}, {"source", ErrInvalidSource},
	{"closed", gordis.ErrClosed}, {"stale", gordis.ErrStale}, {"not_ready", gordis.ErrNotReady},
	{"no_handler", core.ErrNoHandler}, {"next_called", core.ErrNextCalled}, {"next_expired", core.ErrNextExpired},
	{"canceled", context.Canceled}, {"deadline", context.DeadlineExceeded}, {"backpressure", rpc.ErrBackpressure},
}

type wireError struct {
	Codes   []string `json:"codes"`
	Message string   `json:"message"`
}
type reply struct {
	ID     uint64      `json:"id"`
	Error  *wireError  `json:"error"`
	Result *wireResult `json:"result,omitempty"`
	value  core.RemoteResult
}

func errorReply(err error) reply {
	if err == nil {
		return reply{}
	}
	w := &wireError{Message: err.Error(), Codes: []string{}}
	for _, k := range errorKinds {
		if errors.Is(err, k.err) {
			w.Codes = append(w.Codes, k.code)
		}
	}
	if len(w.Codes) == 0 {
		w.Codes = append(w.Codes, "handler")
	}
	return reply{Error: w}
}
func (r reply) err() error {
	if r.Error == nil {
		return nil
	}
	if len(r.Error.Codes) == 0 {
		return ErrInvalidRequest
	}
	errs := []error{errors.New(r.Error.Message)}
	for _, code := range r.Error.Codes {
		if code == "handler" {
			continue
		}
		found := false
		for _, k := range errorKinds {
			if k.code == code {
				errs = append(errs, k.err)
				found = true
				break
			}
		}
		if !found {
			return ErrInvalidRequest
		}
	}
	return errors.Join(errs...)
}

type wireResult struct {
	Payload json.RawMessage `json:"payload"`
	Handled bool            `json:"handled"`
}

type ownerRequest struct {
	Owner core.OwnerRef `json:"owner"`
}
type subscribeRequest struct {
	Owner    core.OwnerRef          `json:"owner"`
	LocalID  uint64                 `json:"local_id"`
	Contract string                 `json:"contract"`
	Kind     core.RemoteHandlerKind `json:"kind"`
	Options  core.RemoteOptions     `json:"options"`
}
type idRequest struct {
	ID uint64 `json:"id"`
}
type dispatchRequest struct {
	Source   *string                 `json:"source,omitempty"`
	Token    uint64                  `json:"token,omitempty"`
	Owner    core.OwnerRef           `json:"owner"`
	Contract string                  `json:"contract"`
	Mode     core.RemoteDispatchMode `json:"mode"`
	Payload  json.RawMessage         `json:"payload"`
}
type deliverRequest struct {
	Token    uint64                 `json:"token,omitempty"`
	ID       uint64                 `json:"id"`
	Owner    core.OwnerRef          `json:"owner"`
	Contract string                 `json:"contract"`
	Kind     core.RemoteHandlerKind `json:"kind"`
	Payload  json.RawMessage        `json:"payload"`
}

type service interface {
	invoke(context.Context, string, json.RawMessage) (reply, error)
}

var hostKey = gordis.NewKey[service]("gordis/rpc/events/private-host")
var childKey = gordis.NewKey[service]("gordis/rpc/events/private-child")
var hostMethods = map[string]rpc.AdmissionClass{
	"RegisterOwner": rpc.Control, "OwnerReady": rpc.Control, "RetireOwner": rpc.Control,
	"Subscribe": rpc.Control, "CommitSubscription": rpc.Control, "Unsubscribe": rpc.Control, "Dispatch": rpc.Business, "ExpireContinuation": rpc.Control, "Continue": rpc.Continuation,
}
var childMethods = map[string]rpc.AdmissionClass{"Enable": rpc.Control, "RevokeSubscription": rpc.Control, "Deliver": rpc.Business, "Continue": rpc.Continuation}

type client struct{ inv rpc.Invoker }

func (c *client) invoke(ctx context.Context, method string, raw json.RawMessage) (reply, error) {
	return c.call(ctx, method, raw)
}

type decodedReply struct {
	value          reply
	method         string
	binding        *Binding
	hook           bool
	requireHandled bool
}

func (r *decodedReply) UnmarshalJSON(raw []byte) error {
	if err := decodeWire(raw, &r.value); err != nil {
		return err
	}
	if errors.Is(r.value.err(), ErrInvalidRequest) {
		return ErrInvalidRequest
	}
	if r.requireHandled && r.value.Error == nil && r.value.Result != nil && !r.value.Result.Handled {
		return ErrInvalidRequest
	}
	if err := r.value.decodeResult(r.binding, r.hook); err != nil {
		return err
	}
	if (r.method != "Subscribe" || r.value.Error != nil) && r.value.ID != 0 || r.method == "Subscribe" && r.value.Error == nil && r.value.ID == 0 {
		return ErrInvalidRequest
	}
	return nil
}

func (c *client) call(ctx context.Context, method string, msg any) (reply, error) {
	result := decodedReply{method: method}
	if err := c.inv.Call(ctx, method, msg, &result); err != nil {
		return reply{}, err
	}
	return result.value, result.value.err()
}
func call(ctx context.Context, peer service, method string, msg any) (reply, error) {
	if caller, ok := peer.(interface {
		call(context.Context, string, any) (reply, error)
	}); ok {
		return caller.call(ctx, method, msg)
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return reply{}, err
	}
	out, err := peer.invoke(ctx, method, raw)
	if err == nil {
		err = out.err()
	}
	return out, err
}

// MarshalJSON is entered by contractInvoker only after Pending admission.
// Codecs that ignore cancellation remain counted until they actually return.
type lazyRequest func() (any, error)

func (f lazyRequest) MarshalJSON() ([]byte, error) {
	msg, err := f()
	if err != nil {
		return nil, err
	}
	return json.Marshal(msg)
}
func serviceBinding(key gordis.Key[service], version string, methods map[string]rpc.AdmissionClass) rpc.Binding {
	return rpc.NewBinding(key, version, rpc.Codec[service]{MethodClasses: methods,
		Client: func(inv rpc.Invoker) service { return &client{inv} },
		Server: func(peer service) rpc.Handler {
			h := rpc.Handler{Methods: map[string]rpc.Unary{}}
			for method := range methods {
				h.Methods[method] = func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
					out, err := peer.invoke(ctx, method, raw)
					if err != nil {
						out = errorReply(err)
					}
					return json.Marshal(out)
				}
			}
			return h
		},
	})
}

// Reject duplicate object keys as well as unknown fields and trailing values.
func strictJSON(raw json.RawMessage, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var scan func() error
	scan = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return err
					}
					key, ok := k.(string)
					if !ok || seen[key] {
						return ErrInvalidRequest
					}
					seen[key] = true
					if err = scan(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := scan(); err != nil {
						return err
					}
				}
			default:
				return ErrInvalidRequest
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := scan(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalidRequest
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func decodeWire(raw json.RawMessage, out any) error {
	if err := strictJSON(raw, out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := required(raw, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	return nil
}
func required(raw json.RawMessage, t reflect.Type) error {
	if t == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return required(raw, t.Elem())
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrInvalidRequest
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			name = f.Name
		}
		v, ok := fields[name]
		if !ok {
			if strings.Contains(f.Tag.Get("json"), ",omitempty") {
				continue
			}
			return ErrInvalidRequest
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) && strings.Contains(f.Tag.Get("json"), ",omitempty") {
			return ErrInvalidRequest
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) && f.Type.Kind() != reflect.Pointer && f.Type != reflect.TypeFor[json.RawMessage]() {
			return ErrInvalidRequest
		}
		if err := required(v, f.Type); err != nil {
			return err
		}
	}
	return nil
}

// Hook results are decoded inside contractInvoker's Pending lifetime.
func callResult(ctx context.Context, peer service, method string, msg any, b Binding, hook bool, waterfall ...bool) (core.RemoteResult, error) {
	if c, ok := peer.(*client); ok {
		out := decodedReply{method: method, binding: &b, hook: hook, requireHandled: len(waterfall) > 0 && waterfall[0]}
		if err := c.inv.Call(ctx, method, msg, &out); err != nil {
			return core.RemoteResult{}, err
		}
		return out.value.value, out.value.err()
	}
	out, err := call(ctx, peer, method, msg)
	if err != nil && out.Result == nil {
		return core.RemoteResult{}, err
	}
	if err := out.decodeResult(&b, hook); err != nil {
		return core.RemoteResult{}, err
	}
	return out.value, errors.Join(err, out.err())
}
func (r *reply) decodeResult(b *Binding, hook bool) error {
	if r.Result == nil {
		if hook && r.Error == nil {
			return ErrInvalidRequest
		}
		return nil
	}
	if !hook || b == nil || b.decodeOutput == nil {
		return ErrInvalidRequest
	}
	value, err := b.decodeOutput(r.Result.Payload)
	if err != nil {
		return err
	}
	r.value = core.RemoteResult{Value: value, Handled: r.Result.Handled}
	return nil
}
func resultReply(b Binding, result core.RemoteResult, err error) (reply, error) {
	r := errorReply(err)
	if b.decl.Kind != "hook" {
		return r, nil
	}
	// No-handler and failures without a result need no output representation.
	if result.Value == nil && err != nil {
		return r, nil
	}
	raw, encodeErr := b.encodeOutput(result.Value)
	if encodeErr != nil {
		return reply{}, encodeErr
	}
	r.Result = &wireResult{Payload: raw, Handled: result.Handled}
	return r, nil
}
