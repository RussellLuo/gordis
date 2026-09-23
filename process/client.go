package process

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

type Options struct {
	Path       string
	Args       []string
	Env        []string
	Identity   Identity
	Instance   string
	Generation uint64
	Config     json.RawMessage
	// Handler exposes explicitly selected host capabilities to this session.
	// It may run during Start, after identity validation, and concurrently.
	Handler        CallHandler
	Limits         Limits
	StartupTimeout time.Duration
	ExitGrace      time.Duration
}

// Diagnostics contains no instance configuration. Forced means Kill was
// requested, not that Kill caused the exit. Reaped, IOComplete and
// HandlersComplete distinguish child exit, pipe cleanup and host handler cleanup.
type Diagnostics struct {
	PID              int    `json:"pid"`
	Session          string `json:"session"`
	Instance         string `json:"instance"`
	Generation       uint64 `json:"generation"`
	Forced           bool   `json:"forced"`
	Reaped           bool   `json:"reaped"`
	IOComplete       bool   `json:"ioComplete"`
	HandlersComplete bool   `json:"handlersComplete"`
	Handling         int    `json:"handling"`
	ExitCode         int    `json:"exitCode"`
	Pending          int    `json:"pending"`
	// StaleResponses also counts discarded requests/cancels for an old session.
	StaleResponses uint64        `json:"staleResponses"`
	Stderr         string        `json:"stderr,omitempty"`
	Error          string        `json:"error,omitempty"`
	CleanupKnown   bool          `json:"cleanupKnown"`
	Cleanup        CleanupResult `json:"cleanup"`
}

type Client struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      *os.File
	stdout     *os.File
	stderr     *os.File
	options    Options
	peer       *peer
	done       <-chan struct{}
	exited     chan struct{}
	cleaned    chan struct{}
	ioWG       sync.WaitGroup
	closeOnce  sync.Once
	failOnce   sync.Once
	failed     chan struct{}
	lifecycle  bool
	closing    bool
	err        error
	cleanupErr error
	diag       Diagnostics
}

// Start waits for identity validation and initialize success. Even unsuccessful
// starts return only after the child, pipe workers, and host handlers have been
// reclaimed. Host handlers must honor cancellation for cleanup to complete.
func Start(ctx context.Context, o Options) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.Instance == "" || o.Generation == 0 || o.Identity.Protocol != Protocol {
		return nil, errors.New("process: invalid session options")
	}
	o.Limits = o.Limits.defaults()
	if o.StartupTimeout <= 0 {
		o.StartupTimeout = 2 * time.Second
	}
	if o.ExitGrace <= 0 {
		o.ExitGrace = 500 * time.Millisecond
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	session := hex.EncodeToString(nonce[:])
	c := &Client{options: o, exited: make(chan struct{}), cleaned: make(chan struct{}), failed: make(chan struct{})}
	c.diag = Diagnostics{Session: session, Instance: o.Instance, Generation: o.Generation, ExitCode: -1}
	c.cmd = exec.Command(o.Path, o.Args...)
	if o.Env != nil {
		c.cmd.Env = o.Env
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	c.stdin, c.stdout, c.stderr = inW, outR, errR
	c.cmd.Stdin, c.cmd.Stdout, c.cmd.Stderr = inR, outW, errW
	err = c.cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, err
	}
	c.diag.PID = c.cmd.Process.Pid
	c.peer = newPeer(outR, inW, o.Limits, message{Session: session, Instance: o.Instance, Generation: o.Generation})
	c.done = c.peer.ctx.Done()
	c.peer.onFailure = c.transportFailed
	verified := false // guarded by peer.mu
	c.peer.accept = func(m message) (invocation, error) {
		if c.peer.closing {
			return invocation{}, ErrClosed
		}
		if !verified {
			return invocation{}, &RPCError{Code: "not_ready", Message: "identity not verified"}
		}
		if reserved(m.Method) {
			if m.Method == "failed" && c.lifecycle {
				var reason string
				if err := json.Unmarshal(m.Params, &reason); err != nil || reason == "" {
					return invocation{}, errors.New("process: invalid failure notification")
				}
				// Record before acknowledging, without destroying cleanup callbacks.
				c.mu.Lock()
				if c.err == nil {
					c.err = fmt.Errorf("process: remote failure: %s", reason)
					c.diag.Error = c.err.Error()
				}
				c.mu.Unlock()
				c.failOnce.Do(func() { close(c.failed) })
				return invocation{control: true, call: func(context.Context) (any, error) { return nil, nil }}, nil
			}
			return invocation{}, &RPCError{Code: "method", Message: "reserved method"}
		}
		return businessCall(o.Handler, c.peer, m), nil
	}
	c.peer.start()
	c.ioWG.Add(1)
	go c.logs()
	go func() {
		err := c.cmd.Wait()
		c.mu.Lock()
		c.diag.Reaped = true
		c.diag.ExitCode = c.cmd.ProcessState.ExitCode()
		c.mu.Unlock()
		close(c.exited)
		if err == nil {
			err = io.EOF
		}
		c.peer.close(fmt.Errorf("process: exited: %w", err))
	}()
	startup, cancel := context.WithTimeout(ctx, o.StartupTimeout)
	defer cancel()
	var identity Identity
	err = c.peer.call(startup, "hello", struct {
		Protocol string `json:"protocol"`
	}{Protocol}, &identity, true)
	if err == nil {
		err = compatible(identity, o.Identity)
	}
	if err == nil {
		c.peer.mu.Lock()
		c.lifecycle = hasContract(identity, LifecycleContract)
		verified = true
		c.peer.mu.Unlock()
		err = c.peer.call(startup, "initialize", o.Config, nil, true)
	}
	if err == nil {
		c.mu.Lock()
		err = c.err
		c.mu.Unlock()
	}
	if err != nil {
		cleanup := c.Close(context.Background())
		return c, errors.Join(err, cleanup)
	}
	return c, nil
}

// Called once by the session, before waking pending calls and watchers.
func (c *Client) transportFailed(err error) {
	c.mu.Lock()
	closing := c.closing
	if !closing {
		c.err = err
		c.diag.Error = err.Error()
	}
	c.mu.Unlock()
	if !closing {
		select {
		case <-c.exited:
		default:
			c.mu.Lock()
			c.diag.Forced = true
			c.mu.Unlock()
			_ = c.cmd.Process.Kill()
		}
	}
}

func (c *Client) logs() {
	defer c.ioWG.Done()
	defer c.stderr.Close()
	buf := make([]byte, 1024)
	for {
		n, err := c.stderr.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.diag.Stderr += string(buf[:n])
			if len(c.diag.Stderr) > 4096 {
				c.diag.Stderr = c.diag.Stderr[len(c.diag.Stderr)-4096:]
			}
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Call invokes a plugin capability. Calls and host handlers can run concurrently.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if closing {
		return ErrClosed
	}
	return c.peer.Call(ctx, method, params, result)
}

// Wait reports a remote runtime or unexpected connection/process failure. Expected Close returns
// nil. This can be installed as a Scope.Go task by an application adapter.
func (c *Client) Wait() error {
	select {
	case <-c.done:
	case <-c.failed:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Watch observes failures as a managed task without owning process cleanup.
// Register Close separately as a final disposer, after request leases drain.
func (c *Client) Watch(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Wait()
	case <-c.failed:
		return c.Wait()
	}
}

// Close rejects new calls and owns an independent child exit grace timer. For
// lifecycle peers, quiesce/dispose precede cancellation of host handlers. It
// keeps cleaning up after ctx expires and waits for all host handlers. Drain
// application request leases before Close; remote cleanup errors are retained.
func (c *Client) Close(ctx context.Context) error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		if !c.lifecycle {
			c.peer.stopCalls()
		}
		go func() {
			grace, cancel := context.WithTimeout(context.Background(), c.options.ExitGrace)
			defer cancel()
			if c.lifecycle {
				var result CleanupResult
				err := c.peer.call(grace, "quiesce", nil, nil, true)
				if err == nil {
					err = c.peer.call(grace, "dispose", nil, &result, true)
				}
				c.mu.Lock()
				if err == nil {
					c.diag.CleanupKnown, c.diag.Cleanup = true, result
					if !result.Complete || result.Error != "" {
						c.cleanupErr = fmt.Errorf("process: remote cleanup incomplete or failed: %s", result.Error)
					}
				} else {
					c.cleanupErr = fmt.Errorf("process: remote cleanup unknown: %w", err)
				}
				c.mu.Unlock()
				// Dispose has replied only after plugin work and callbacks drain.
				c.peer.stopCalls()
			}
			_ = c.peer.call(grace, "shutdown", nil, nil, true)
			// Signal EOF after shutdown as well. A plugin may be blocked reading
			// inherited blocking stdin while its shutdown handler closes it.
			_ = c.stdin.Close()
			select {
			case <-c.exited:
			case <-grace.Done():
				c.mu.Lock()
				c.diag.Forced = true
				c.mu.Unlock()
				if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					c.mu.Lock()
					c.cleanupErr = errors.Join(c.cleanupErr, err)
					c.mu.Unlock()
				}
				<-c.exited
			}
			c.peer.close(ErrClosed)
			// No descendants are supported. Closing local ends also guarantees a
			// misbehaving inherited pipe cannot strand our readers after Wait.
			_ = c.stdin.Close()
			_ = c.stdout.Close()
			_ = c.stderr.Close()
			c.peer.workers.Wait()
			c.ioWG.Wait()
			c.mu.Lock()
			c.diag.IOComplete = true
			c.mu.Unlock()
			c.peer.handlers.Wait()
			c.mu.Lock()
			c.diag.HandlersComplete = true
			c.mu.Unlock()
			close(c.cleaned)
		}()
	})
	select {
	case <-c.cleaned:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.cleanupErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Snapshot() Diagnostics {
	c.mu.Lock()
	d := c.diag
	d.Cleanup.Details = append(json.RawMessage(nil), d.Cleanup.Details...)
	c.mu.Unlock()
	c.peer.mu.Lock()
	d.Pending = len(c.peer.pending)
	d.Handling = len(c.peer.running)
	d.StaleResponses = c.peer.stale
	c.peer.mu.Unlock()
	return d
}
