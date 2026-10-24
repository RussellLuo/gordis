package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/RussellLuo/gordis"
)

type testService interface {
	Call(context.Context, string) (string, error)
}
type testServiceFunc func(context.Context, string) (string, error)

func (f testServiceFunc) Call(ctx context.Context, msg string) (string, error) { return f(ctx, msg) }

var testImportKey = gordis.NewKey[testService]("test/private-import")
var testExportKey = gordis.NewKey[testService]("test/private-export")

func testServiceBinding(key gordis.Key[testService]) Binding {
	return NewBinding(key, "1", Codec[testService]{
		Client: func(inv Invoker) testService {
			return testServiceFunc(func(ctx context.Context, msg string) (string, error) {
				var out string
				err := inv.Call(ctx, "call", msg, &out)
				return out, err
			})
		},
		Server: func(service testService) Handler {
			return Handler{Methods: map[string]Unary{"call": func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				var msg string
				if err := decode(raw, &msg); err != nil {
					return nil, err
				}
				out, err := service.Call(ctx, msg)
				if err != nil {
					return nil, err
				}
				return json.Marshal(out)
			}}}
		},
	})
}
func TestLocalImportValidation(t *testing.T) {
	binding := testServiceBinding(testImportKey)
	for _, value := range []any{nil, testServiceFunc(nil), 1} {
		if _, err := BindLocalImport(binding, value); err == nil {
			t.Fatalf("accepted %T", value)
		}
	}
	valid, err := BindLocalImport(binding, testServiceFunc(func(context.Context, string) (string, error) { return "", nil }))
	requireNoError(t, err)
	for _, mode := range []string{"duplicate", "outside", "version", "type", "zero", "uncovered"} {
		t.Run(mode, func(t *testing.T) {
			options := AttachOptions{Command: Command{Path: "must-not-start"}, ChildBoundary: Boundary{Imports: []Binding{binding}}, LocalImports: []LocalImport{valid}}
			switch mode {
			case "duplicate":
				options.LocalImports = append(options.LocalImports, valid)
			case "outside":
				options.ChildBoundary.Imports = nil
			case "version":
				b := binding
				b.version = "2"
				options.ChildBoundary.Imports = []Binding{b}
			case "type":
				options.ChildBoundary.Imports = []Binding{NewBinding(gordis.NewKey[int](testImportKey.Name()), "1", Codec[int]{Client: func(Invoker) int { return 1 }, Server: func(int) Handler { return Handler{} }})}
			case "zero":
				options.LocalImports = []LocalImport{{}}
			case "uncovered":
				options.LocalImports = nil
			}
			p := &testPlugin{spec: gordis.PluginSpec{ID: "loader"}, activate: func(ctx context.Context, s *gordis.Scope) error {
				sess, err := Attach(ctx, s, options)
				if err == nil || sess != nil {
					t.Errorf("expected pre-acquisition failure: %v %+v", err, sess)
				}
				return errors.New("expected activation failure")
			}}
			host, err := gordis.NewHost(p)
			requireNoError(t, err)
			defer host.Shutdown(context.Background())
			_, _ = host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "loader", Plugin: "loader"})
		})
	}
}
