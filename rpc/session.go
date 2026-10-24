package rpc

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

// Limits configures one peer of a process Session. Host settings belong in
// ProcessOptions/AttachOptions; Child settings belong in ServeUnitOptions.
// Settings are not negotiated or copied to the other peer. An outbound call
// must fit the caller's Pending and the receiver's Handling limits separately.
//
// Admission fields use zero for unlimited and reject negative values. Message
// size and duration fields instead use their documented defaults when <= 0.
// Ordinary service calls have no default deadline; supply a context deadline.
type Limits struct {
	// MaxMessage sets send/receive byte limits on this peer's RPC servers and
	// the Host's outbound connection, including envelope/encoding overhead.
	// Streams are checked per message, not by total lifetime bytes. Values <= 0
	// select 4 MiB. This field is not applied to the Child's broker callback
	// client; that connection uses go-plugin's client defaults and remains subject
	// to the Host callback server's limits. This is not a Session memory cap;
	// oversize messages also map to ErrBackpressure/ResourceExhausted.
	MaxMessage int

	// Business limits ordinary unary calls, reverse callbacks and streams, plus
	// event Dispatch/Deliver. All contracts in the Session share these slots;
	// long streams or slow services can occupy capacity needed by other calls.
	// Size for concurrent work and synchronous callback/reentry paths, not QPS.
	// Calls waiting for nested Business calls retain their slots, so finite caps
	// may reject nested work even when the receiver could otherwise execute it.
	// Zero Pending/Handling fields are independently unlimited.
	Business AdmissionLimit

	// Control limits bounded state-management methods explicitly classified by
	// Codec.MethodClasses. It is independent of Business and Continuation, so
	// long business handlers/continuations do not consume management slots.
	// RPC's Initialize/Quiesce/Dispose/Watch do not consume this budget. Ordinary
	// cleanup service callbacks still use their declared class and may be rejected
	// when that class is full. Zero fields are independently unlimited.
	Control AdmissionLimit

	// Continuation limits explicitly classified next/base calls for existing
	// invocations. Size for simultaneous and nested continuations; outer calls
	// retain their slots until they return. A finite budget supports only the
	// nesting/concurrency that fits it, not arbitrary reentry. This capacity is
	// neither borrowed from Business nor shared with Control. Zero fields are
	// independently unlimited.
	Continuation AdmissionLimit

	// StartupTimeout bounds Host discovery/Attach startup, beginning before
	// import validation and including remote initialization and Accept. An earlier
	// caller deadline wins; custom validators/Accept must honor cancellation.
	// Values <= 0 select 5 seconds. This does not set business-call deadlines.
	StartupTimeout time.Duration

	// StopTimeout bounds Host stop-control RPC waits: the OnStop Quiesce attempt
	// and the final Quiesce/Dispose sequence each get their own timeout.
	// Values <= 0 select 4 seconds. It is not a deadline for all local cleanup:
	// actual callbacks or serializers ignoring cancellation can delay final drain
	// after Child reaping. Bound the caller's Shutdown wait separately as needed.
	StopTimeout time.Duration

	// OrphanGrace bounds the Child's cleanup grace after the main connection or
	// parent process is lost, or ServeUnit's context is canceled. On expiry it
	// stops serving even if cleanup has not completed. Values <= 0 select 2 seconds.
	// This Child-side setting does not bound normal Host Dispose or prove cleanup.
	OrphanGrace time.Duration
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
	// Limits configures the Host peer, including outbound export calls and
	// inbound import callbacks. Configure the Child's ServeUnitOptions separately.
	Limits Limits
	// LocalImports are private callbacks constructed for this activation.
	LocalImports []LocalImport
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
	Admission AdmissionSnapshot `json:"admission"`
	// CallbackTransportClosed records completed callback transport shutdown.
	CallbackTransportClosed bool `json:"callback_transport_closed"`
	// CallbacksClosed means no new callbacks can enter.
	CallbacksClosed bool `json:"callbacks_closed"`
	// CallbacksDrained requires closed admission and zero actual handlers;
	// Child cleanup, reaping and outbound Pending remain separate evidence.
	CallbacksDrained bool `json:"callbacks_drained"`
	// CallbackHandling counts actual handlers, including canceled calls.
	// Zero while admission is open means idle, not drained.
	CallbackHandling int               `json:"callback_handling"`
	Identity         Identity          `json:"identity"`
	Session          SessionInfo       `json:"session"`
	PID              int               `json:"pid"`
	Ready            bool              `json:"ready"`
	Reaped           bool              `json:"reaped"`
	Cleanup          *CleanupReport    `json:"cleanup,omitempty"`
	Nodes            []gordis.Snapshot `json:"nodes,omitempty"`
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
	scope                   *gordis.Scope
	admission               *admission
	stopping                bool
	prepareOnce             sync.Once
	prepared                *PreparedExports
	prepareErr              error
	callbacks               *callbackServer
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
	diagnostics := s.diagnostics
	if s.admission != nil {
		diagnostics.Admission = s.admission.snapshot()
	}
	if s.callbacks != nil {
		diagnostics.CallbacksClosed, diagnostics.CallbackHandling, diagnostics.CallbackTransportClosed = s.callbacks.snapshot()
		diagnostics.CallbacksDrained = diagnostics.CallbacksClosed && diagnostics.CallbackHandling == 0
	}
	data, _ := json.Marshal(diagnostics)
	var d Diagnostics
	_ = json.Unmarshal(data, &d)
	return d
}
func (s *Session) Exited() bool { return s != nil && s.client != nil && s.client.Exited() }
func (s *Session) invoker(b Binding) Invoker {
	return &contractInvoker{conn: s.remote.conn, info: s.info, contract: b.Spec().Name(), classes: b.classes, admission: s.admission}
}

// Attach installs teardown before process acquisition. Ordinary imports resolve
// through Scope.Get and must be declared in Requires. LocalImports supply only
// private activation-owned callbacks; their declarations remain in ChildBoundary.
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
	if err := options.Limits.validate(); err != nil {
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
	locals := map[string]LocalImport{}
	for _, local := range options.LocalImports {
		if err := local.binding.valid(); err != nil {
			return nil, err
		}
		name := local.binding.Spec().Name()
		if _, exists := locals[name]; exists {
			return nil, fmt.Errorf("rpc: duplicate local import %s", name)
		}
		found := false
		for _, binding := range options.ChildBoundary.Imports {
			if binding.Spec() == local.binding.Spec() && binding.Version() == local.binding.Version() && binding.check(local.value) {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("rpc: local import outside boundary: %s", name)
		}
		locals[name] = local
	}
	handlers := map[string]boundHandler{}
	for _, binding := range options.ChildBoundary.Imports {
		var handler Handler
		var err error
		if local, ok := locals[binding.Spec().Name()]; ok {
			handler, err = binding.local(ctx, local.value)
		} else {
			handler, err = binding.resolve(ctx, scope)
		}
		if err != nil {
			return nil, err
		}
		handlers[binding.Spec().Name()] = boundHandler{Handler: handler, classes: binding.classes}
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	session := &Session{admission: newAdmission(limits), scope: scope, info: SessionInfo{hex.EncodeToString(nonce), scope.ID(), scope.Generation()}, identity: options.Identity, boundary: Boundary{Imports: append([]Binding(nil), options.ChildBoundary.Imports...), Exports: append([]Binding(nil), options.ChildBoundary.Exports...)}}
	session.diagnostics = Diagnostics{Identity: options.Identity, Session: session.info}
	client, cmd := newProgramClient(options.Command, limits)
	stopCancel := context.AfterFunc(ctx, client.Kill)
	defer stopCancel()
	session.client = client
	callbacks := new(callbackServer)
	session.callbacks = callbacks
	session.closeFunc = func() error {
		session.mu.Lock()
		session.stopping = true
		session.admission.closePending()
		session.mu.Unlock()
		defer func() {
			client.Kill()
			session.mu.Lock()
			session.diagnostics.Ready = false
			session.diagnostics.Reaped = client.Exited()
			session.mu.Unlock()
			callbacks.stop()
			session.admission.close()
			session.admission.wait()
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
	release, err := scope.Acquire()
	if err != nil {
		return session, err
	}
	defer release()
	if err = scope.OnStop("rpc quiesce", func(context.Context) error {
		session.mu.Lock()
		if session.stopping {
			session.mu.Unlock()
			return nil
		}
		session.stopping = true
		session.admission.closePending()
		remote, ready := session.remote, session.diagnostics.Ready
		session.mu.Unlock()
		if remote == nil || !ready {
			cancel() // Initialization owns rollback; Quiesce requires completed Initialize.
			return nil
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), limits.StopTimeout)
		defer cancel()
		return invoke(stopCtx, remote.conn, "Quiesce", packet{Session: session.info}, nil)
	}); err != nil {
		return session, err
	}
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
	session.mu.Lock()
	session.remote = raw.(*remote)
	session.mu.Unlock()
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
	session.mu.Lock()
	if session.stopping {
		session.mu.Unlock()
		return session, gordis.ErrClosed
	}
	session.initializationAttempted = true
	session.mu.Unlock()
	err = invoke(ctx, session.remote.conn, "Initialize", packet{Identity: options.Identity, Session: session.info, Bindings: decl, Broker: broker, Config: config}, &result)
	if err != nil {
		return session, err
	}
	if result.Session != session.info {
		return session, errors.New("Ready identity mismatch")
	}
	session.mu.Lock()
	if session.stopping {
		session.mu.Unlock()
		return session, gordis.ErrClosed
	}
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
	mu               sync.Mutex
	server           *grpc.Server
	stopped          bool
	handling         int
	transportStopped bool
	work             sync.WaitGroup
}

func (c *callbackServer) install(server *grpc.Server) {
	c.mu.Lock()
	stopped := c.stopped
	c.server = server
	if stopped {
		c.transportStopped = false
	}
	c.mu.Unlock()
	if stopped {
		server.Stop()
		c.mu.Lock()
		c.transportStopped = true
		c.mu.Unlock()
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
	c.work.Wait()
	c.mu.Lock()
	c.transportStopped = true
	c.mu.Unlock()
}

func (s *Session) serveCallbacks(handlers map[string]boundHandler, limits Limits, callbacks *callbackServer) uint32 {
	if len(handlers) == 0 {
		return 0
	}
	broker := s.remote.broker.NextId()
	go s.remote.broker.AcceptAndServe(broker, func(options []grpc.ServerOption) *grpc.Server {
		options = append(options, grpc.MaxRecvMsgSize(limits.MaxMessage), grpc.MaxSendMsgSize(limits.MaxMessage), grpc.WaitForHandlers(true))
		server := grpc.NewServer(options...)
		registerRPC(server, &callbackRouter{info: s.info, handlers: handlers, tracker: callbacks, admission: s.admission})
		callbacks.install(server)
		return server
	})
	return broker
}

type contractInvoker struct {
	conn      grpc.ClientConnInterface
	info      SessionInfo
	contract  string
	classes   map[string]AdmissionClass
	admission *admission
}

func (v *contractInvoker) Call(c context.Context, method string, in, out any) error {
	done, e := v.enter(c, method, false)
	if e != nil {
		return e
	}
	defer done()
	raw, e := json.Marshal(in)
	if e != nil {
		return e
	}
	if err := c.Err(); err != nil {
		return err
	}
	return invoke(c, v.conn, "Call", packet{Session: v.info, Contract: v.contract, Method: method, Payload: raw}, out)
}
func (v *contractInvoker) Stream(c context.Context, method string, in any, yield func(json.RawMessage) error) error {
	done, e := v.enter(c, method, true)
	if e != nil {
		return e
	}
	defer done()
	raw, e := json.Marshal(in)
	if e != nil {
		return e
	}
	if err := c.Err(); err != nil {
		return err
	}
	return receive(c, v.conn, "Stream", packet{Session: v.info, Contract: v.contract, Method: method, Payload: raw}, yield)
}

// enter tracks actual callback lifetime, including handlers outliving their
// canceled clients. Fixed imports remain callable through Child Dispose.
func (c *callbackServer) enter() (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return nil, gordis.ErrClosed
	}
	c.handling++
	c.work.Add(1)
	return func() { c.mu.Lock(); c.handling--; c.mu.Unlock(); c.work.Done() }, nil
}
func (c *callbackServer) snapshot() (closed bool, handling int, transportClosed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped, c.handling, c.transportStopped
}

func (v *contractInvoker) enter(ctx context.Context, method string, stream bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	class := v.classes[method]
	if stream && class != Business {
		return nil, errors.New("rpc: stream must use Business admission")
	}
	return v.admission.enter(ctx, class, true)
}
