package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var testIdentity = Identity{Protocol, "test.plugin", "1", []string{"test/1"}}

// This helper runs in a real independent OS process, including adversarial peers.
func TestProcessChild(t *testing.T) {
	mode := os.Getenv("GORDIS_PROCESS_CHILD")
	if mode == "" {
		return
	}
	if mode == "hanghello" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if mode == "raw" ||
		mode == "wait-eof" ||
		mode == "early" ||
		mode == "noexit" ||
		mode == "noread" ||
		mode == "oversize" {
		scan := bufio.NewScanner(os.Stdin)
		enc := json.NewEncoder(os.Stdout)
		for scan.Scan() {
			var m message
			_ = json.Unmarshal(scan.Bytes(), &m)
			if m.Type == "cancel" {
				continue
			}
			m.Type = "response"
			switch m.Method {
			case "hello":
				if mode == "early" {
					callback := m
					callback.Type, callback.Method, callback.ID = "request", "host.early", 1
					_ = enc.Encode(callback)
					if !scan.Scan() {
						os.Exit(24)
					}
					var response message
					if json.Unmarshal(scan.Bytes(), &response) != nil || response.Error == nil || response.Error.Code != "not_ready" {
						os.Exit(25)
					}
				}
				m.Result, _ = json.Marshal(testIdentity)
			case "initialize":
				m.Result = json.RawMessage(`null`)
			case "shutdown":
				if mode == "noexit" {
					time.Sleep(time.Hour)
				}
				_ = enc.Encode(m)
				if mode == "wait-eof" {
					_, _ = io.Copy(io.Discard, os.Stdin)
				}
				os.Exit(0)
			default:
				if mode == "oversize" {
					fmt.Println(strings.Repeat("x", 128*1024))
					time.Sleep(time.Hour)
				}
				stale := m
				stale.Session = "old-session"
				_ = enc.Encode(stale)
				stale = m
				stale.Generation++
				_ = enc.Encode(stale)
				m.Result = json.RawMessage(`"current"`)
			}
			_ = enc.Encode(m)
			if mode == "noread" && m.Method == "initialize" {
				time.Sleep(time.Hour)
			}
		}
		os.Exit(0)
	}
	identity := testIdentity
	if mode == "badidentity" {
		identity.Version = "wrong"
	}
	if mode == "protocol-mismatch" {
		identity.Protocol = "gordis.process/0"
	}
	err := Serve(context.Background(), os.Stdin, os.Stdout, identity, Limits{MaxPending: 64, Queue: 64}, Handler{
		Initialize: func(ctx context.Context, host Caller, _ json.RawMessage) error {
			if mode == "duplex" || mode == "callback-init-failure" {
				return host.Call(ctx, "host.init", nil, nil)
			}
			if mode == "hanginit" {
				time.Sleep(time.Hour)
			}
			if mode == "reject" {
				return errors.New("configuration rejected")
			}
			return nil
		},
		Call: func(ctx context.Context, host Caller, method string, raw json.RawMessage) (any, error) {
			switch method {
			case "identity":
				return raw, nil
			case "relay":
				var call struct {
					Method string
					Value  json.RawMessage
				}
				if err := json.Unmarshal(raw, &call); err != nil {
					return nil, err
				}
				var result json.RawMessage
				err := host.Call(ctx, call.Method, call.Value, &result)
				return result, err
			case "cancel-host":
				short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				err := host.Call(short, "wait", nil, nil)
				// The propagated millisecond deadline may expire on the host first.
				return err != nil, nil
			case "panic":
				panic("plugin panic")
			case "crash":
				os.Exit(23)
			case "wait":
				<-ctx.Done()
				return nil, ctx.Err()
			case "late":
				time.Sleep(80 * time.Millisecond)
				return "late", nil
			case "logs":
				fmt.Fprint(os.Stderr, strings.Repeat("log", 10000))
			}
			return "ok", nil
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func startChild(t *testing.T, mode string, limits Limits, handlers ...CallHandler) (*Client, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var handler CallHandler
	if len(handlers) > 0 {
		handler = handlers[0]
	}
	// Race binaries sleep for a second at exit by default; that artificial delay
	// would hide graceful shutdown behind this test's short kill grace period.
	c, err := Start(ctx, Options{
		Handler: handler,
		Path:    exe,
		Args:    []string{"-test.run=^TestProcessChild$"},
		Env: append(
			os.Environ(),
			"GORDIS_PROCESS_CHILD="+mode,
			"GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0",
		),
		Identity:       testIdentity,
		Instance:       "alpha",
		Generation:     1,
		Limits:         limits,
		StartupTimeout: 150 * time.Millisecond,
		ExitGrace:      60 * time.Millisecond,
	})
	if c != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := c.Close(ctx); err != nil {
				t.Error(err)
			}
			d := c.Snapshot()
			if !d.Reaped || !d.IOComplete || !d.HandlersComplete || d.Handling != 0 || d.Pending != 0 {
				t.Error(d)
			}
		})
	}
	return c, err
}

func TestHandshakeFailuresReapChild(t *testing.T) {
	for _, mode := range []string{"badidentity", "protocol-mismatch", "hanghello", "hanginit", "reject", "duplex"} {
		t.Run(mode, func(t *testing.T) {
			c, err := startChild(t, mode, Limits{})
			if err == nil {
				t.Fatal("accepted")
			}
			if c == nil || !c.Snapshot().Reaped || !c.Snapshot().IOComplete {
				t.Fatal(c, err)
			}
		})
	}
}

func TestCancellationLateResponsesAndIndependentCalls(t *testing.T) {
	c, err := startChild(t, "normal", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- c.Call(ctx, "late", nil, nil) }()
	for c.Snapshot().Pending == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var got string
	if err := c.Call(context.Background(), "echo", nil, &got); err != nil || got != "ok" {
		t.Fatal(got, err)
	}
	end := time.Now().Add(time.Second)
	for c.Snapshot().StaleResponses == 0 {
		if time.Now().After(end) {
			t.Fatal("late response not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if err := c.Call(context.Background(), "logs", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.Snapshot().Stderr) > 4096 {
		t.Fatal("unbounded logs")
	}
}

func TestSessionAndGenerationIsolation(t *testing.T) {
	c, err := startChild(t, "raw", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	err = c.Call(context.Background(), "echo", nil, &got)
	if err != nil || got != "current" || c.Snapshot().StaleResponses != 2 {
		t.Fatal(got, err, c.Snapshot())
	}
}

func TestProcessCrashOversizeAndForcedExit(t *testing.T) {
	for _, mode := range []string{"normal", "oversize", "noexit"} {
		t.Run(mode, func(t *testing.T) {
			c, err := startChild(t, mode, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "noexit" {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				method := "crash"
				if mode == "oversize" {
					method = "oversize"
				}
				if err := c.Call(ctx, method, nil, nil); err == nil {
					t.Fatal("peer failure ignored")
				}
				if c.Wait() == nil {
					t.Fatal("missing failure")
				}
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
				defer cancel()
				if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			}
			if err := c.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			d := c.Snapshot()
			if !d.Reaped || !d.IOComplete || (mode == "noexit" && !d.Forced) {
				t.Fatal(d)
			}
		})
	}
}

func TestPendingAndQueueBackpressure(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		c, err := startChild(t, "normal", Limits{MaxPending: 1})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- c.Call(ctx, "wait", nil, nil) }()
		for c.Snapshot().Pending == 0 {
			time.Sleep(time.Millisecond)
		}
		if err := c.Call(context.Background(), "echo", nil, nil); !errors.Is(err, ErrBackpressure) {
			t.Fatal(err)
		}
		cancel()
		<-done
	})
	t.Run("queue", func(t *testing.T) {
		c, err := startChild(t, "noread", Limits{Queue: 1, MaxPending: 64})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		results := make(chan error, 32)
		var wg sync.WaitGroup
		for n := 0; n < 32; n++ {
			wg.Add(1)
			go func() { defer wg.Done(); results <- c.Call(ctx, "large", strings.Repeat("x", 32000), nil) }()
		}
		wg.Wait()
		close(results)
		backpressure := false
		for err := range results {
			backpressure = backpressure || errors.Is(err, ErrBackpressure)
		}
		if !backpressure {
			t.Fatal("unbounded queue")
		}
	})
}

func TestConcurrentCorrelation(t *testing.T) {
	c, err := startChild(t, "normal", Limits{MaxPending: 64, Queue: 64})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 32; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var got string
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := c.Call(ctx, "echo", nil, &got); err != nil || got != "ok" {
				t.Errorf("%q %v", got, err)
			}
		}()
	}
	wg.Wait()
}

func TestShutdownClosesStdinBeforeWaitingForExit(t *testing.T) {
	c, err := startChild(t, "wait-eof", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := c.Snapshot(); d.Forced || d.ExitCode != 0 {
		t.Fatal(d)
	}
}
