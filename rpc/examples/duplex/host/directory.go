package host

import (
	"context"
	"fmt"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/examples/duplex/contract"
)

type DirectoryPlugin struct{}

func (*DirectoryPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       contract.DirectoryPluginID,
		Provides: []gordis.ServiceSpec{contract.DirectoryKey.Spec()},
		New:      func() gordis.Plugin { return new(DirectoryPlugin) },
	}
}

func (*DirectoryPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide[contract.UserDirectory](contract.DirectoryKey, directory{})
}

type directory struct{}

func (directory) DisplayName(_ context.Context, userID string) (string, error) {
	if userID != "user-42" {
		return "", fmt.Errorf("unknown user %q", userID)
	}
	const name = "Gordis"
	if _, err := fmt.Printf("callback: %s -> %s\n", userID, name); err != nil {
		return "", err
	}
	return name, nil
}
