// Package contract is ordinary business API; it does not import RPC.
package contract

import (
	"context"
	"encoding/json"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc/internal/genfixture/dto"
)

//go:generate go run ../../../cmd/rpcgen -type Service -key Key -version Version -package bindings -out ../bindings/service.go

type version string

const Version version = "1"

type Health interface {
	Ping(context.Context) error
}

type Service interface {
	Health
	Value(context.Context) (int, error)
	Greet(context.Context, string, int) (string, error)
	Echo(context.Context, dto.Value) (dto.Value, error)
	EchoJSON(context.Context, json.RawMessage) (dto.Raw, error)
	JSONStream(context.Context, json.RawMessage, func(dto.Raw) error) error
	Empty(context.Context, struct{}) (struct{}, error)
	Nullable(context.Context, *dto.Node) (*dto.Node, error)
	Wait(context.Context) error
	Fail(context.Context) error
}

var Key = gordis.NewKey[Service]("genfixture/service")
