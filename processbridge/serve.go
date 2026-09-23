package processbridge

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/RussellLuo/gordis"
	internalplugin "github.com/RussellLuo/gordis/internal/plugin"
	"github.com/RussellLuo/gordis/process"
)

type ServeOptions struct {
	Identity           process.Identity
	Limits             process.Limits
	Plugin             gordis.Plugin
	Requires, Provides []Binding
}

// Serve runs one Plugin/Start with the Host's immutable instance and
// generation. It never creates a child Host. Only declared bindings are exposed.
func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser, o ServeOptions) error {
	spec, err := internalplugin.Inspect(o.Plugin)
	if err != nil {
		return err
	}
	if err := match(spec.Requires, o.Requires); err != nil {
		return err
	}
	if err := match(spec.Provides, o.Provides); err != nil {
		return err
	}
	o.Identity = identityWithBindings(o.Identity, o.Requires, o.Provides)
	var r *pluginRun
	var handler process.CallHandler
	var notify sync.Once
	return process.Serve(ctx, in, out, o.Identity, o.Limits, process.Handler{
		InitializeSession: func(ctx context.Context, peer process.Caller, s process.Session, raw json.RawMessage) error {
			bindings := map[string]any{}
			for _, b := range o.Requires {
				bindings[b.spec.Name()] = b.client(boundCaller{peer, b.spec.Name()})
			}
			r = newPluginRun(spec, s.Instance, s.Generation, bindings, func(err error) {
				// Do not wait for RPC while the failing task still owns a lifecycle ref.
				notify.Do(func() {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						_ = process.ReportFailure(ctx, peer, err)
					}()
				})
			})
			if err := r.start(ctx, spec, raw); err != nil {
				return err
			}
			handlers := map[string]process.CallHandler{}
			for _, b := range o.Provides {
				handlers[b.spec.Name()] = b.handler(r.values[b.spec.Name()])
			}
			handler = router(handlers)
			return nil
		},
		Admit: func(string) (func(), error) { return r.controller.Scope().Acquire() },
		Call: func(ctx context.Context, peer process.Caller, method string, raw json.RawMessage) (any, error) {
			return handler(ctx, peer, method, raw)
		},
		Quiesce: func() {
			if r != nil {
				r.quiesce()
			}
		},
		Dispose: func() process.CleanupResult {
			if r == nil {
				return process.CleanupResult{Complete: true}
			}
			err := r.dispose()
			snapshot := r.controller.Snapshot()
			details, _ := json.Marshal(snapshot)
			result := process.CleanupResult{Complete: snapshot.CleanupComplete, Details: details}
			if err != nil {
				result.Error = err.Error()
			}
			return result
		},
	})
}
