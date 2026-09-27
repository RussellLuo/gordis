package processbridge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/internal/lifecycle"
	internalplugin "github.com/RussellLuo/gordis/internal/plugin"
)

// pluginRun drives exactly one externally allocated generation. It owns no
// graph, process, transport or generation counter.
type pluginRun struct {
	controller               *lifecycle.Controller
	values                   map[string]any
	quiesceOnce, disposeOnce sync.Once
	disposeErr               error
}

func newPluginRun(
	spec gordis.PluginSpec,
	id string,
	generation uint64,
	bindings map[string]any,
	fail func(error),
) *pluginRun {
	r := &pluginRun{}
	r.controller = lifecycle.New(id, generation, spec.Requires, spec.Provides, bindings, func(err error) {
		r.controller.Freeze()
		if fail != nil {
			fail(err)
		}
	})
	return r
}

func (r *pluginRun) start(ctx context.Context, spec gordis.PluginSpec, raw json.RawMessage) error {
	err := lifecycle.Invoke(func() error {
		p, err := internalplugin.Prepare(spec, raw)
		if err != nil {
			return err
		}
		scope := &gordis.Scope{Scope: r.controller.Scope()}
		if a, ok := p.(interface {
			Activate(context.Context, *gordis.Scope) error
		}); ok {
			return a.Activate(ctx, scope)
		}
		return errors.New("processbridge: plugin does not implement Activate")
	})
	err, _ = r.controller.Commit(ctx, err, nil, func(values map[string]any) { r.values = values })
	return err
}

func (r *pluginRun) quiesce() {
	r.controller.Freeze()
	r.quiesceOnce.Do(r.controller.Quiesce)
}

func (r *pluginRun) dispose() error {
	r.quiesce()
	r.disposeOnce.Do(func() { r.disposeErr = r.controller.Dispose(); r.values = nil })
	return r.disposeErr
}
