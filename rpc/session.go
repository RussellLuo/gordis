package gordisrpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/RussellLuo/gordis"
	plugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

type Identity struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Protocol string `json:"protocol"`
}

func (i Identity) validate() error {
	if i.ID == "" || i.Version == "" || i.Protocol != Protocol {
		return errors.New("invalid id/version/protocol")
	}
	return nil
}

type SessionInfo struct {
	ID             string `json:"id"`
	HostInstance   string `json:"host_instance"`
	HostGeneration uint64 `json:"host_generation"`
}
type Limits struct {
	MaxMessage                               int
	StartupTimeout, StopTimeout, OrphanGrace time.Duration
}

func (l Limits) defaults() Limits {
	if l.MaxMessage <= 0 {
		l.MaxMessage = 4 * 1024 * 1024
	}
	if l.StartupTimeout <= 0 {
		l.StartupTimeout = 5 * time.Second
	}
	if l.StopTimeout <= 0 {
		l.StopTimeout = 4 * time.Second
	}
	if l.OrphanGrace <= 0 {
		l.OrphanGrace = 2 * time.Second
	}
	return l
}

type Command struct {
	Path      string
	Args, Env []string
}
type AttachOptions struct {
	Command Command
	// Identity optionally pins the expected program; zero discovers it during handshake.
	Identity Identity
	Config   json.RawMessage
	// ChildBoundary declares the expected child imports and exports, using
	// the same child-relative directions as Unit.Boundary(). It must match
	// the child's complete set of contract names, versions and directions.
	ChildBoundary Boundary
	Limits        Limits
	// Accept validates/publishes domain contributions after remote Ready.
	// A failure synchronously rolls back the process before Attach returns.
	Accept func(context.Context, *Session) error
}
type CleanupReport struct {
	Session         SessionInfo `json:"session"`
	CleanupComplete bool        `json:"cleanup_complete"`
	Forced          bool        `json:"forced"`
	Errors          []string    `json:"errors,omitempty"`
}
type Diagnostics struct {
	Identity Identity          `json:"identity"`
	Session  SessionInfo       `json:"session"`
	PID      int               `json:"pid"`
	Ready    bool              `json:"ready"`
	Reaped   bool              `json:"reaped"`
	Cleanup  *CleanupReport    `json:"cleanup,omitempty"`
	Nodes    []gordis.Snapshot `json:"nodes,omitempty"`
}

// description is exchanged before constructing or activating business instances.
type description struct {
	Identity     Identity
	Bindings     []declaration
	ConfigSchema json.RawMessage
}

type ready struct {
	Session SessionInfo
	Nodes   []gordis.Snapshot
}
type event struct{ Kind, Instance, Error string }
type Session struct {
	initializationAttempted bool
	mu                      sync.Mutex
	info                    SessionInfo
	identity                Identity
	boundary                Boundary
	client                  *plugin.Client
	remote                  *remote
	diagnostics             Diagnostics
	closeOnce               sync.Once
	closeFunc               func() error
	closeErr                error
}

func (s *Session) Snapshot() Diagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(s.diagnostics)
	var d Diagnostics
	_ = json.Unmarshal(data, &d)
	return d
}
func (s *Session) Exited() bool { return s != nil && s.client != nil && s.client.Exited() }
func (s *Session) invoker(b Binding) Invoker {
	return &contractInvoker{conn: s.remote.conn, info: s.info, contract: b.Spec().Name()}
}

// Attach installs teardown before process acquisition. Imports are resolved
// through Scope.Get, so callers must declare RequiredServices in Requires.
func Attach(ctx context.Context, scope *gordis.Scope, options AttachOptions) (attached *Session, err error) {
	if scope == nil {
		return nil, errors.New("nil scope")
	}
	if options.Identity != (Identity{}) {
		if err := options.Identity.validate(); err != nil {
			return nil, err
		}
	}
	config, _, err := normalizeProgramConfig(options.Config)
	if err != nil {
		return nil, err
	}
	limits := options.Limits.defaults()
	ctx, cancel := context.WithTimeout(ctx, limits.StartupTimeout)
	defer cancel()
	decl, err := declarations(options.ChildBoundary)
	if err != nil {
		return nil, err
	}
	if options.Command.Path == "" {
		return nil, errors.New("empty command")
	}
	handlers := map[string]boundHandler{}
	for _, binding := range options.ChildBoundary.Imports {
		handler, err := binding.resolve(ctx, scope)
		if err != nil {
			return nil, err
		}
		handlers[binding.Spec().Name()] = boundHandler{Handler: handler}
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	session := &Session{info: SessionInfo{hex.EncodeToString(nonce), scope.ID(), scope.Generation()}, identity: options.Identity, boundary: Boundary{Imports: append([]Binding(nil), options.ChildBoundary.Imports...), Exports: append([]Binding(nil), options.ChildBoundary.Exports...)}}
	session.diagnostics = Diagnostics{Identity: options.Identity, Session: session.info}
	client, cmd := newProgramClient(options.Command, limits)
	stopCancel := context.AfterFunc(ctx, client.Kill)
	defer stopCancel()
	session.client = client
	callbacks := new(callbackServer)
	session.closeFunc = func() error {
		defer func() {
			client.Kill()
			session.mu.Lock()
			session.diagnostics.Ready = false
			session.diagnostics.Reaped = client.Exited()
			session.mu.Unlock()
			callbacks.stop()
		}()
		return session.dispose(limits.StopTimeout)
	}
	closeSession := func() error {
		session.closeOnce.Do(func() { session.closeErr = session.closeFunc() })
		return session.closeErr
	}
	if err = scope.Defer("rpc process", func(context.Context) error { return closeSession() }); err != nil {
		client.Kill()
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeSession())
		}
	}()
	connection, err := client.Client()
	stopCancel()
	if cmd.Process != nil {
		session.mu.Lock()
		session.diagnostics.PID = cmd.Process.Pid
		session.mu.Unlock()
	}
	if err != nil {
		return session, err
	}
	raw, err := connection.Dispense("unit")
	if err != nil {
		return session, err
	}
	session.remote = raw.(*remote)
	described, schema, err := describeRemote(ctx, session.remote)
	if err != nil {
		return session, err
	}
	if options.Identity != (Identity{}) && options.Identity != described.Identity {
		return session, errors.New("identity mismatch")
	}
	if !reflect.DeepEqual(decl, described.Bindings) {
		return session, errors.New("boundary mismatch")
	}
	options.Identity = described.Identity
	session.identity = described.Identity
	session.mu.Lock()
	session.diagnostics.Identity = described.Identity
	session.mu.Unlock()

	if err = schema.validate(config); err != nil {
		return session, err
	}

	broker := session.serveCallbacks(handlers, limits, callbacks)
	var result ready
	session.initializationAttempted = true
	err = invoke(ctx, session.remote.conn, "Initialize", packet{Identity: options.Identity, Session: session.info, Bindings: decl, Broker: broker, Config: config}, &result)
	if err != nil {
		return session, err
	}
	if result.Session != session.info {
		return session, errors.New("Ready identity mismatch")
	}
	session.mu.Lock()
	session.diagnostics.Ready = true
	session.diagnostics.Nodes = result.Nodes
	session.mu.Unlock()
	if options.Accept != nil {
		if err = options.Accept(ctx, session); err != nil {
			return session, err
		}
	}
	err = scope.AfterReady("rpc watch", session.watch)
	return session, err
}

// dispose keeps callbacks alive until the child has reported its actual cleanup.
// Process reaping and callback shutdown are performed by the caller afterwards.
func (s *Session) dispose(timeout time.Duration) error {
	if s.remote == nil || !s.initializationAttempted {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.diagnostics.PID != 0 {
			s.diagnostics.Cleanup = &CleanupReport{Session: s.info, Forced: true, Errors: []string{"RPC startup failed before initialization"}}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	quiesceErr := invoke(ctx, s.remote.conn, "Quiesce", packet{Session: s.info}, nil)
	var report CleanupReport
	err := invoke(ctx, s.remote.conn, "Dispose", packet{Session: s.info}, &report)
	if err != nil {
		report = CleanupReport{Session: s.info, Forced: true, Errors: []string{err.Error()}}
	} else if report.Session != s.info || !report.CleanupComplete || report.Forced || len(report.Errors) != 0 {
		err = errors.New("cleanup incomplete")
	}
	s.mu.Lock()
	s.diagnostics.Cleanup = &report
	s.mu.Unlock()
	return errors.Join(quiesceErr, err)
}

func (s *Session) watch(ctx context.Context) error {
	err := receive(ctx, s.remote.conn, "Watch", packet{Session: s.info}, func(raw json.RawMessage) error {
		var notification event
		if err := decode(raw, &notification); err != nil {
			return err
		}
		if notification.Kind == "terminated" {
			return nil
		}
		return fmt.Errorf("unit %s instance=%s: %s", notification.Kind, notification.Instance, notification.Error)
	})
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unit transport/failure: %w", err)
	}
	return errors.New("unit terminated while Host ready")
}

// The broker may construct its server concurrently with failed initialization.
// A late server must be stopped too; holding the mutex during Stop could block
// handlers that are still completing their Host callbacks.
type callbackServer struct {
	mu      sync.Mutex
	server  *grpc.Server
	stopped bool
}

func (c *callbackServer) install(server *grpc.Server) {
	c.mu.Lock()
	stopped := c.stopped
	c.server = server
	c.mu.Unlock()
	if stopped {
		server.Stop()
	}
}
func (c *callbackServer) stop() {
	c.mu.Lock()
	c.stopped = true
	server := c.server
	c.mu.Unlock()
	if server != nil {
		server.Stop()
	}
}

func (s *Session) serveCallbacks(handlers map[string]boundHandler, limits Limits, callbacks *callbackServer) uint32 {
	if len(handlers) == 0 {
		return 0
	}
	broker := s.remote.broker.NextId()
	go s.remote.broker.AcceptAndServe(broker, func(options []grpc.ServerOption) *grpc.Server {
		options = append(options, grpc.MaxRecvMsgSize(limits.MaxMessage), grpc.MaxSendMsgSize(limits.MaxMessage), grpc.WaitForHandlers(true))
		server := grpc.NewServer(options...)
		registerRPC(server, &callbackRouter{info: s.info, handlers: handlers})
		callbacks.install(server)
		return server
	})
	return broker
}

type contractInvoker struct {
	conn     grpc.ClientConnInterface
	info     SessionInfo
	contract string
}

func (v *contractInvoker) Call(c context.Context, method string, in, out any) error {
	raw, e := json.Marshal(in)
	if e != nil {
		return e
	}
	return invoke(c, v.conn, "Call", packet{Session: v.info, Contract: v.contract, Method: method, Payload: raw}, out)
}
func (v *contractInvoker) Stream(c context.Context, method string, in any, yield func(json.RawMessage) error) error {
	raw, e := json.Marshal(in)
	if e != nil {
		return e
	}
	return receive(c, v.conn, "Stream", packet{Session: v.info, Contract: v.contract, Method: method, Payload: raw}, yield)
}
