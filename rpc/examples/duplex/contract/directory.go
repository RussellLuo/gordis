package contract

import (
	"context"

	"github.com/RussellLuo/gordis"
)

//go:generate go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type UserDirectory -key DirectoryKey -version Version -package bindings -out ../bindings/directory.go

const DirectoryPluginID = "example/duplex/directory-provider"

type UserDirectory interface {
	DisplayName(context.Context, string) (string, error)
}

var DirectoryKey = gordis.NewKey[UserDirectory]("example/duplex/directory")
