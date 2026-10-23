// Package contract defines the ordinary Greeter API shared by both programs.
package contract

import (
	"context"

	"github.com/RussellLuo/gordis"
)

//go:generate go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key Key -version Version -package bindings -out ../bindings/greeter.go

const PluginID = "example/basic/greeter-provider"
const Version = "1"

type Greeter interface {
	Greet(context.Context, string) (string, error)
}

var Key = gordis.NewKey[Greeter]("example/basic/greeter")
