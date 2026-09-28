// Package greeter implements a regular Gordis Plugin that consumes a
// UserDirectory and provides a Greeter. It contains no process-specific code.
package greeter

import (
	"context"

	"github.com/RussellLuo/gordis"
)

const PluginID = "greeter"

type UserDirectory interface {
	DisplayName(context.Context, string) (string, error)
}

type Greeter interface {
	Greet(context.Context, string) (string, error)
}

var UserDirectoryKey = gordis.NewKey[UserDirectory]("example.duplex.user-directory/1")
var GreeterKey = gordis.NewKey[Greeter]("example.duplex.greeter/1")

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{UserDirectoryKey.Spec()},
		Provides: []gordis.ServiceSpec{GreeterKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	directory, err := gordis.Get(scope, UserDirectoryKey)
	if err != nil {
		return err
	}
	return gordis.Provide(scope, GreeterKey, Greeter(service{directory: directory}))
}

type service struct {
	directory UserDirectory
}

func (s service) Greet(ctx context.Context, userID string) (string, error) {
	name, err := s.directory.DisplayName(ctx, userID)
	if err != nil {
		return "", err
	}
	return "Hello, " + name + "!", nil
}

var _ gordis.Plugin = (*Plugin)(nil)
var _ Greeter = service{}
