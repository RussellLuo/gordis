package events

import (
	"context"
	"errors"
	"runtime"

	"github.com/RussellLuo/gordis"
)

const PluginID = "gordis.events"

// Bus is the non-generic service used by Binding's typed methods. Its methods
// are intentionally private so lifecycle and routing invariants stay inside
// this package.
type Bus interface {
	subscribe(*gordis.Scope, Contract, any, subscriptionOptions) (func(), error)
	dispatch(context.Context, *gordis.Scope, contractSpec, any, dispatchKind, *sourceAddress, any) (any, bool, error)
}

// BusKey is the ordinary Gordis Service key required by event users.
var BusKey = gordis.NewKey[Bus]("gordis.events.Bus")

// Plugin provides one local Bus. MaxParallel limits the number of notification
// handlers running concurrently across parallel publications on this Bus.
// Zero selects GOMAXPROCS, with a minimum of one.
type Plugin struct {
	MaxParallel int `json:"maxParallel,omitempty"`
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Provides: []gordis.ServiceSpec{BusKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (p *Plugin) Validate() error {
	if p.MaxParallel < 0 {
		return errors.New("events: maxParallel must not be negative")
	}
	return nil
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	parallelism := p.MaxParallel
	if parallelism == 0 {
		parallelism = runtime.GOMAXPROCS(0)
		if parallelism < 1 {
			parallelism = 1
		}
	}
	bus := newLocalBus(parallelism)
	if err := scope.Defer("events bus", func(context.Context) error {
		bus.close()
		return nil
	}); err != nil {
		return err
	}
	return gordis.Provide[Bus](scope, BusKey, bus)
}
