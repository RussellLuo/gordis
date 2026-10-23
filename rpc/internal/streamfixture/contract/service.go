// Package contract contains ordinary APIs shared by the generated fixture.
package contract

import (
	"context"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
)

//go:generate go run ../../../cmd/rpcgen -type Generator -key GeneratorKey -version Version -package bindings -out ../bindings/generator.go
//go:generate go run ../../../cmd/rpcgen -type Inspector -key InspectorKey -version Version -package bindings -out ../bindings/inspector.go
//go:generate go run ../../../cmd/rpcgen -type Callback -key CallbackKey -version Version -package bindings -out ../bindings/callback.go

const Version = "1"

type Yield = func(*dto.Node) error
type Nullable interface {
	Nullable(context.Context, Yield) error
}
type Generator interface {
	Nullable
	Run(context.Context, string, int, func(dto.Value) error) error
}
type Inspector interface {
	Wait(context.Context, string) error
}
type Callback interface {
	Call(context.Context, string) (string, error)
}

var GeneratorKey = gordis.NewKey[Generator]("streamfixture/generator")
var InspectorKey = gordis.NewKey[Inspector]("streamfixture/inspector")
var CallbackKey = gordis.NewKey[Callback]("streamfixture/callback")
