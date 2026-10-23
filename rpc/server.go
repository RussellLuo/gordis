package gordisrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/RussellLuo/gordis"
	plugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type ServeUnitOptions struct {
	Identity Identity
	Limits   Limits
	Unit     Unit
}

// ServeUnit is the standard standalone-program entry. Unit.Config maps program
// configuration to local instances. Cancelling ctx stops the server and bounds cleanup.
func ServeUnit(ctx context.Context, o ServeUnitOptions) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := o.Identity.validate(); e != nil {
		return e
	}
	if e := o.Unit.validate(); e != nil {
		return e
	}
	o.Limits = o.Limits.defaults()
	server := &unitServer{options: o, done: make(chan struct{})}
	parent := os.Getppid()
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: handshake, Plugins: map[string]plugin.Plugin{"unit": &transport{server: server}}, GRPCServer: func(opts []grpc.ServerOption) *grpc.Server {
		opts = append(opts, grpc.MaxRecvMsgSize(o.Limits.MaxMessage), grpc.MaxSendMsgSize(o.Limits.MaxMessage), grpc.StatsHandler(server))
		s := grpc.NewServer(opts...)
		server.grpc = s
		// ConnEnd covers established sessions. Parent identity also covers
		// the interval before Initialize (or even before the first dial).
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-server.done:
					return
				case <-ticker.C:
					if os.Getppid() != parent || parent == 1 {
						server.lose()
						return
					}
				}
			}
		}()
		go func() {
			select {
			case <-ctx.Done():
				server.lose()
			case <-server.done:
			}
		}()
		return s
	}})
	return nil
}

type connKey struct{}
type connToken struct{ value byte }
type unitServer struct {
	mu           sync.Mutex
	options      ServeUnitOptions
	broker       *plugin.GRPCBroker
	grpc         *grpc.Server
	owner        *connToken
	info         SessionInfo
	initialized  bool
	initializing chan struct{}
	lost, ready  bool
	host         *gordis.Host
	handlers     map[string]boundHandler
	cancel       context.CancelFunc
	domain       context.Context
	stop         sync.Once
	loss         sync.Once
	done         chan struct{}
	cleanup      CleanupReport
}

func (*unitServer) TagRPC(c context.Context, _ *stats.RPCTagInfo) context.Context { return c }
func (*unitServer) HandleRPC(context.Context, stats.RPCStats)                     {}
func (*unitServer) TagConn(c context.Context, _ *stats.ConnTagInfo) context.Context {
	return context.WithValue(c, connKey{}, new(connToken))
}
func (s *unitServer) HandleConn(c context.Context, e stats.ConnStats) {
	if _, ok := e.(*stats.ConnEnd); !ok {
		return
	}
	s.mu.Lock()
	owner := s.owner != nil && s.owner == c.Value(connKey{})
	s.mu.Unlock()
	if owner {
		s.lose()
	}
}
func (s *unitServer) lose() {
	s.loss.Do(func() {
		s.mu.Lock()
		s.lost = true
		init := s.initializing
		s.mu.Unlock()
		go func() {
			timer := time.NewTimer(s.options.Limits.OrphanGrace)
			defer timer.Stop()
			if init != nil {
				select {
				case <-init:
				case <-timer.C:
					s.grpc.Stop()
					return
				}
			}
			s.beginStop()
			select {
			case <-s.done:
			case <-timer.C:
			}
			s.grpc.Stop()
		}()
	})
}
func (s *unitServer) initialize(c context.Context, p packet) (*wrapperspb.BytesValue, error) {
	s.mu.Lock()
	if s.initialized || s.lost {
		s.mu.Unlock()
		return nil, errors.New("already initialized or disconnected")
	}
	expected, e := declarations(s.options.Unit.Boundary())
	if e != nil || p.Identity != s.options.Identity || p.Session.ID == "" || p.Session.HostInstance == "" || p.Session.HostGeneration == 0 || !reflect.DeepEqual(expected, p.Bindings) {
		s.mu.Unlock()
		return nil, errors.New("identity/boundary mismatch")
	}
	if (len(s.options.Unit.Imports) > 0) != (p.Broker != 0) {
		s.mu.Unlock()
		return nil, errors.New("callback boundary mismatch")
	}
	s.initialized = true
	s.info = p.Session
	s.owner, _ = c.Value(connKey{}).(*connToken)
	s.initializing = make(chan struct{})
	init := s.initializing
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.initializing = nil; close(init); s.mu.Unlock() }()
	var conn *grpc.ClientConn
	if p.Broker != 0 {
		conn, e = s.broker.Dial(p.Broker)
		if e != nil {
			return nil, e
		}
	}
	s.domain, s.cancel = context.WithCancel(context.Background())
	h, handlers, e := assemble(c, s.options.Unit, p.Config, func(b Binding) Invoker { return &contractInvoker{conn: conn, info: s.info, contract: b.Spec().Name()} })
	s.host, s.handlers = h, handlers
	if conn != nil { // Keep imported dependencies available until all child cleanup completes.
		go func() { <-s.done; _ = conn.Close() }()
	}
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	return encode(ready{Session: s.info, Nodes: h.Snapshot()})
}
func (s *unitServer) check(info SessionInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized || s.initializing != nil || info != s.info {
		return errors.New("wrong or initializing session")
	}
	return nil
}
func (s *unitServer) beginStop() {
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	s.stop.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		go func() {
			r := CleanupReport{Session: s.info}
			if s.host != nil {
				if e := s.host.Shutdown(context.Background()); e != nil {
					r.Errors = append(r.Errors, e.Error())
				}
				for _, n := range s.host.Snapshot() {
					if !n.CleanupComplete || n.CleanupError != "" || n.Leases != 0 || len(n.Residuals) > 0 {
						r.Errors = append(r.Errors, "incomplete: "+n.QualifiedID)
					}
				}
			}
			r.CleanupComplete = len(r.Errors) == 0
			s.mu.Lock()
			s.cleanup = r
			s.mu.Unlock()
			close(s.done)
		}()
	})
}
func (s *unitServer) acquire(c context.Context, p packet) (boundHandler, context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready || p.Session != s.info {
		return boundHandler{}, nil, nil, unavailable()
	}
	h, ok := s.handlers[p.Contract]
	if !ok {
		return boundHandler{}, nil, nil, status.Error(codes.PermissionDenied, "undeclared contract")
	}
	release, e := h.scope.AcquireReady()
	if e != nil {
		return boundHandler{}, nil, nil, e
	}
	c, cancel := context.WithCancel(c)
	stop := context.AfterFunc(s.domain, cancel)
	return h, c, func() { stop(); cancel(); release() }, nil
}
func (s *unitServer) unary(c context.Context, method string, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	var p packet
	if e := decode(in.Value, &p); e != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid envelope")
	}
	if method == "Describe" {
		bindings, err := declarations(s.options.Unit.Boundary())
		if err != nil {
			return nil, err
		}
		return encode(description{Identity: s.options.Identity, Bindings: bindings, ConfigSchema: s.options.Unit.Config.schemaJSON()})
	}
	if method == "Initialize" {
		return s.initialize(c, p)
	}
	if method == "Call" {
		h, c, done, e := s.acquire(c, p)
		if e != nil {
			return nil, e
		}
		defer done()
		return call(c, h.Handler, p)
	}
	if e := s.check(p.Session); e != nil {
		return nil, e
	}
	switch method {
	case "Quiesce":
		s.beginStop()
		return encode(struct{}{})
	case "Dispose":
		s.beginStop()
		select {
		case <-c.Done():
			return nil, c.Err()
		case <-s.done:
			s.mu.Lock()
			defer s.mu.Unlock()
			return encode(s.cleanup)
		}
	}
	return nil, status.Error(codes.Unimplemented, "unknown method")
}
func (s *unitServer) stream(method string, in *wrapperspb.BytesValue, stream grpc.ServerStream) error {
	var p packet
	if e := decode(in.Value, &p); e != nil {
		return e
	}
	if method == "Stream" {
		h, c, done, e := s.acquire(stream.Context(), p)
		if e != nil {
			return e
		}
		defer done()
		return streaming(c, h.Handler, p, stream)
	}
	if e := s.check(p.Session); e != nil {
		return e
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.host != nil {
			for _, n := range s.host.Snapshot() {
				if n.Failure != "" {
					s.beginStop()
					v, _ := encode(event{Kind: "failed", Instance: n.QualifiedID, Error: n.Failure})
					return stream.SendMsg(v)
				}
			}
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-s.done:
			v, _ := encode(event{Kind: "terminated"})
			return stream.SendMsg(v)
		case <-ticker.C:
		}
	}
}
func call(c context.Context, h Handler, p packet) (*wrapperspb.BytesValue, error) {
	f := h.Methods[p.Method]
	if f == nil {
		return nil, status.Error(codes.Unimplemented, "unknown method")
	}
	raw, e := f(c, p.Payload)
	if e != nil {
		return nil, rpcError(e)
	}
	if !json.Valid(raw) {
		return nil, status.Error(codes.Internal, "invalid response JSON")
	}
	return wrapperspb.Bytes(raw), nil
}
func streaming(c context.Context, h Handler, p packet, s grpc.ServerStream) error {
	f := h.Streams[p.Method]
	if f == nil {
		return status.Error(codes.Unimplemented, "unknown stream")
	}
	return rpcError(f(c, p.Payload, func(raw json.RawMessage) error {
		if !json.Valid(raw) {
			return fmt.Errorf("invalid stream JSON")
		}
		return s.SendMsg(wrapperspb.Bytes(raw))
	}))
}

type callbackRouter struct {
	info     SessionInfo
	handlers map[string]boundHandler
}

func (r *callbackRouter) unary(c context.Context, method string, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	var p packet
	if e := decode(in.Value, &p); e != nil {
		return nil, e
	}
	if p.Session != r.info {
		return nil, errors.New("wrong callback session")
	}
	h, ok := r.handlers[p.Contract]
	if !ok || method != "Call" {
		return nil, status.Error(codes.PermissionDenied, "undeclared callback")
	}
	return call(c, h.Handler, p)
}
func (r *callbackRouter) stream(method string, in *wrapperspb.BytesValue, s grpc.ServerStream) error {
	var p packet
	if e := decode(in.Value, &p); e != nil {
		return e
	}
	h, ok := r.handlers[p.Contract]
	if p.Session != r.info || !ok || method != "Stream" {
		return status.Error(codes.PermissionDenied, "undeclared callback")
	}
	return streaming(s.Context(), h.Handler, p, s)
}
