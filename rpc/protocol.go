package gordisrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const Protocol = "gordis-rpc/1"
const serviceName = "gordis.rpc.v1.Unit"

// The envelope is carried in the standard protobuf BytesValue. JSON encodes
// explicit control/domain DTOs, never Go objects. The wire is private/versioned.
type packet struct {
	Identity Identity        `json:"identity,omitempty"`
	Session  SessionInfo     `json:"session"`
	Bindings []declaration   `json:"bindings,omitempty"`
	Broker   uint32          `json:"broker,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
	Contract string          `json:"contract,omitempty"`
	Method   string          `json:"method,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

func encode(v any) (*wrapperspb.BytesValue, error) {
	b, e := json.Marshal(v)
	return wrapperspb.Bytes(b), e
}
func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

type rpcService interface {
	unary(context.Context, string, *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error)
	stream(string, *wrapperspb.BytesValue, grpc.ServerStream) error
}

func registerRPC(s *grpc.Server, impl rpcService) {
	desc := grpc.ServiceDesc{ServiceName: serviceName, HandlerType: (*rpcService)(nil)}
	for _, name := range []string{"Describe", "Initialize", "Quiesce", "Dispose", "Call"} {
		desc.Methods = append(desc.Methods, grpc.MethodDesc{MethodName: name, Handler: func(srv any, c context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			in := new(wrapperspb.BytesValue)
			if e := dec(in); e != nil {
				return nil, e
			}
			handler := func(c context.Context, r any) (any, error) {
				return srv.(rpcService).unary(c, name, r.(*wrapperspb.BytesValue))
			}
			if interceptor == nil {
				return handler(c, in)
			}
			return interceptor(c, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + serviceName + "/" + name}, handler)
		}})
	}
	for _, name := range []string{"Watch", "Stream"} {
		desc.Streams = append(desc.Streams, grpc.StreamDesc{StreamName: name, ServerStreams: true, Handler: func(srv any, stream grpc.ServerStream) error {
			in := new(wrapperspb.BytesValue)
			if e := stream.RecvMsg(in); e != nil {
				return e
			}
			return srv.(rpcService).stream(name, in, stream)
		}})
	}
	s.RegisterService(&desc, impl)
}
func invoke(c context.Context, conn grpc.ClientConnInterface, method string, in, out any) error {
	p, e := encode(in)
	if e != nil {
		return e
	}
	v := new(wrapperspb.BytesValue)
	if e = conn.Invoke(c, "/"+serviceName+"/"+method, p, v); e != nil {
		return contextError(e)
	}
	if out == nil {
		return nil
	}
	return decode(v.Value, out)
}
func receive(c context.Context, conn grpc.ClientConnInterface, method string, in any, yield func(json.RawMessage) error) error {
	p, e := encode(in)
	if e != nil {
		return e
	}
	c, cancel := context.WithCancel(c)
	defer cancel()
	stream, e := conn.NewStream(c, &grpc.StreamDesc{ServerStreams: true}, "/"+serviceName+"/"+method)
	if e != nil {
		return e
	}
	if e = stream.SendMsg(p); e != nil {
		return e
	}
	if e = stream.CloseSend(); e != nil {
		return e
	}
	for {
		v := new(wrapperspb.BytesValue)
		e = stream.RecvMsg(v)
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return contextError(e)
		}
		if e = yield(append(json.RawMessage(nil), v.Value...)); e != nil {
			return e
		}
	}
}
func contextError(e error) error {
	switch status.Code(e) {
	case codes.Canceled:
		return errors.Join(context.Canceled, e)
	case codes.DeadlineExceeded:
		return errors.Join(context.DeadlineExceeded, e)
	}
	return e
}
func rpcError(e error) error {
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return status.FromContextError(e).Err()
	}
	return e
}
func unavailable() error { return status.Error(codes.Unavailable, "unit unavailable") }
