package gordisrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	plugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func programEvent(event string) {
	f, err := os.OpenFile(os.Getenv("C02_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, event)
}

type programConfig struct {
	Prefix string `json:"prefix,omitempty" jsonschemax:"minLength=1"`
	Mode   string `json:"mode,omitempty"`
}

func (programConfig) New() programConfig { programEvent("new"); return programConfig{Prefix: "Hello"} }
func (c programConfig) Configure(context.Context) (map[string]any, error) {
	programEvent("configure")
	if c.Mode == "business-error" {
		return nil, errors.New("business constraint")
	}
	mapping := map[string]any{"value": map[string]string{"prefix": c.Prefix}, "other": map[string]string{"prefix": c.Prefix}}
	if c.Mode == "unknown-target" {
		mapping["missing"] = struct{}{}
	}
	if c.Mode == "bad-mapping" {
		mapping["other"] = nil
	}
	return mapping, nil
}

type programPlugin struct {
	ID     string `json:"-"`
	Prefix string `json:"prefix"`
}

func (p *programPlugin) Spec() gordis.PluginSpec {
	s := gordis.PluginSpec{ID: p.ID, New: func() gordis.Plugin { return &programPlugin{ID: p.ID} }}
	if p.ID == "value" {
		s.Provides = []gordis.ServiceSpec{gordis.NewKey[string]("test/value").Spec()}
	}
	return s
}
func (p *programPlugin) Validate() error {
	if p.Prefix == "internal-invalid" {
		return errors.New("internal plugin validation")
	}
	return nil
}
func (p *programPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	programEvent("activate:" + p.ID + ":" + p.Prefix)
	if err := scope.Defer("record", func(context.Context) error { programEvent("cleanup:" + p.ID); return nil }); err != nil {
		return err
	}
	if p.ID == "value" {
		return scope.Provide(gordis.NewKey[string]("test/value"), p.Prefix)
	}
	return nil
}

type programRouter struct {
	*unitServer
	mode string
}

func (s *programRouter) unary(ctx context.Context, method string, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	programEvent(method)
	if method == "Describe" {
		if s.mode == "cancel" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if s.mode == "bad-schema" {
			return encode(description{Identity: s.options.Identity, ConfigSchema: json.RawMessage(`{"type":"array"}`)})
		}
		if s.mode == "unknown-envelope" {
			return wrapperspb.Bytes([]byte(`{"Identity":{"id":"test","version":"1","protocol":"gordis-rpc/1"},"ConfigSchema":{"type":"object"},"extra":true}`)), nil
		}
	}
	return s.unitServer.unary(ctx, method, in)
}

type programTransport struct {
	plugin.NetRPCUnsupportedPlugin
	router *programRouter
}

func (p *programTransport) GRPCServer(b *plugin.GRPCBroker, s *grpc.Server) error {
	p.router.broker = b
	registerRPC(s, p.router)
	return nil
}
func (*programTransport) GRPCClient(context.Context, *plugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, errors.New("server only")
}

// Run the race-instrumented test executable as a real child with observable
// author methods and business lifecycle, including deliberately bad descriptions.
func TestProgramChild(t *testing.T) {
	if os.Getenv("C02_CHILD") == "" {
		return
	}
	programEvent("pid:" + strconv.Itoa(os.Getpid()))
	config, err := NewConfig[programConfig]()
	if err != nil {
		panic(err)
	}
	unit := Unit{Plugins: []gordis.Plugin{&programPlugin{ID: "value"}, &programPlugin{ID: "other"}}, Instances: []gordis.InstanceSpec{{ID: "value", Plugin: "value"}, {ID: "other", Plugin: "other"}}, Config: config, Exports: []Export{{Instance: "value", Binding: valueBinding(nil)}}}
	options := ServeUnitOptions{Identity: Identity{ID: "test/program", Version: "1", Protocol: Protocol}, Unit: unit, Limits: Limits{}.defaults()}
	mode := os.Getenv("C02_CHILD")
	if mode == "invalid-unit" {
		options.Unit.Instances = append(options.Unit.Instances, options.Unit.Instances[0])
		if err := ServeUnit(context.Background(), options); err == nil {
			panic("invalid Unit accepted")
		}
		os.Exit(0)
	}
	server := &unitServer{options: options, done: make(chan struct{})}
	router := &programRouter{server, mode}
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: handshake, Plugins: map[string]plugin.Plugin{"unit": &programTransport{router: router}}, GRPCServer: func(opts []grpc.ServerOption) *grpc.Server {
		s := grpc.NewServer(opts...)
		server.grpc = s
		return s
	}})
	os.Exit(0)
}
func programCommand(t *testing.T, mode string) (Command, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "events")
	return Command{Path: exe, Args: []string{"-test.run=^TestProgramChild$"}, Env: []string{"C02_CHILD=" + mode, "C02_LOG=" + log}}, log
}
func programEvents(t *testing.T, log string) string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func assertProgramReaped(t *testing.T, log string) string {
	t.Helper()
	events := programEvents(t, log)
	line := strings.Split(events, "\n")[0]
	pid, err := strconv.Atoi(strings.TrimPrefix(line, "pid:"))
	if err != nil {
		t.Fatal(events)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d remains: %v", pid, err)
	}
	return events
}
func TestDescribeProgramLifecycle(t *testing.T) {
	if Protocol != "gordis-rpc/1" || handshake.ProtocolVersion != 1 || handshake.MagicCookieValue != Protocol {
		t.Fatal("protocol changed")
	}
	for _, mode := range []string{"normal", "bad-schema", "unknown-envelope", "invalid-unit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			command, log := programCommand(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if mode == "cancel" {
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							raw, _ := os.ReadFile(log)
							if strings.Contains(string(raw), "Describe") {
								cancel()
								return
							}
						}
					}
				}()
			}
			d, err := DescribeProgram(ctx, command, Limits{})
			if (err == nil) != (mode == "normal") {
				t.Fatalf("description: %+v %v", d, err)
			}
			if mode == "normal" {
				if len(d.Contracts) != 1 || d.Contracts[0].ID != "test/value" || d.Contracts[0].Direction != "export" || d.Identity.ID != "test/program" {
					t.Fatalf("description: %+v", d)
				}
				if _, err := compileConfigSchema(d.ConfigSchema); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			events := assertProgramReaped(t, log)
			for _, forbidden := range []string{"new\n", "configure\n", "Initialize", "activate:", "cleanup:"} {
				if strings.Contains(events, forbidden) {
					t.Fatalf("probe ran business: %s", events)
				}
			}
		})
	}
}

// Bypass Host validation and send Initialize directly over the real transport.
func TestChildProgramConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		calls     bool
	}{
		{`{"prefix":""}`, "program config schema", false},
		{`{"unknown":true}`, "program config schema", false},
		{`null`, "program config decode", false},
		{`{"mode":"business-error"}`, "Configure", true},
		{`{"mode":"unknown-target"}`, "instance mapping", true},
		{`{"mode":"bad-mapping"}`, "instance mapping", true},
		{`{"prefix":"internal-invalid"}`, "internal plugin validation", true},
		{`{}`, "", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			command, log := programCommand(t, "normal")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _ := newProgramClient(command, Limits{}.defaults())
			defer client.Kill()
			connection, err := client.Client()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := connection.Dispense("unit")
			if err != nil {
				t.Fatal(err)
			}
			remote := raw.(*remote)
			d, _, err := describeRemote(ctx, remote)
			if err != nil {
				t.Fatal(err)
			}
			info := SessionInfo{ID: "session", HostInstance: "host", HostGeneration: 1}
			var result ready
			err = invoke(ctx, remote.conn, "Initialize", packet{Identity: d.Identity, Session: info, Bindings: d.Bindings, Config: json.RawMessage(tc.raw)}, &result)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Initialize: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			events := programEvents(t, log)
			wantCalls := 0
			if tc.calls {
				wantCalls = 1
			}
			if strings.Count(events, "new\n") != wantCalls || strings.Count(events, "configure\n") != wantCalls {
				t.Fatal(events)
			}
			if tc.want != "" && strings.Contains(events, "activate:") {
				t.Fatalf("partial activation: %s", events)
			}
			if tc.want == "" && (!strings.Contains(events, "activate:value:Hello") || !strings.Contains(events, "activate:other:Hello")) {
				t.Fatal(events)
			}
			if err := invoke(ctx, remote.conn, "Dispose", packet{Session: info}, nil); err != nil {
				t.Fatal(err)
			}
			client.Kill()
			assertProgramReaped(t, log)
		})
	}
}

type captureProgram struct {
	*ProcessPlugin
	attached chan *ProcessPlugin
}

func (p *captureProgram) Spec() gordis.PluginSpec {
	s := p.ProcessPlugin.Spec()
	newInstance := s.New
	s.New = func() gordis.Plugin { return &captureProgram{newInstance().(*ProcessPlugin), p.attached} }
	return s
}
func (p *captureProgram) Activate(ctx context.Context, scope *gordis.Scope) error {
	err := p.ProcessPlugin.Activate(ctx, scope)
	p.attached <- p.ProcessPlugin
	return err
}
func TestHostProgramConfigValidation(t *testing.T) {
	for _, mode := range []string{"bad-schema", "normal"} {
		t.Run(mode, func(t *testing.T) {
			command, log := programCommand(t, mode)
			prototype, err := NewProcess(ProcessOptions{PluginID: "process", Command: command, ChildBoundary: Boundary{Exports: []Binding{valueBinding(nil)}}})
			if err != nil {
				t.Fatal(err)
			}
			attached := make(chan *ProcessPlugin, 1)
			host, err := gordis.NewHost(&captureProgram{prototype, attached})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Shutdown(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err = host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "proxy", Plugin: "process", Config: json.RawMessage(`{"prefix":""}`)}); err == nil {
				t.Fatal("invalid config accepted")
			}
			p := <-attached
			if !p.Snapshot().Reaped {
				t.Fatal("not reaped")
			}
			events := assertProgramReaped(t, log)
			if strings.Contains(events, "Initialize") || strings.Contains(events, "new\n") || strings.Contains(events, "cleanup:") {
				t.Fatalf("Host failed to reject before Initialize: %s", events)
			}
		})
	}
}

func TestInvalidUnitPrecedesConfigMethods(t *testing.T) {
	spec := configSpecFor[lifecycleConfig](t)
	unit := Unit{Plugins: []gordis.Plugin{valuePlugin()}, Instances: []gordis.InstanceSpec{{ID: "value", Plugin: "value"}, {ID: "value", Plugin: "value"}}, Config: spec}
	resetConfigCalls()
	if host, _, err := assemble(context.Background(), unit, nil, nil); err == nil || host != nil {
		t.Fatal("invalid Unit assembled")
	}
	checkConfigCalls(t, 0, 0)
}
