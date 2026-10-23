// Package contract defines ordinary APIs shared by both programs.
package contract

import (
	"context"

	"github.com/RussellLuo/gordis"
)

//go:generate go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key GreeterKey -version Version -package bindings -out ../bindings/greeter.go

const GreeterPluginID = "example/duplex/greeter-provider"
const Version = "1"

type Greeter interface {
	Greet(context.Context, string) (string, error)
}

var GreeterKey = gordis.NewKey[Greeter]("example/duplex/greeter")
