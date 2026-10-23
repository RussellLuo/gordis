// Package streamfixture declares a test program without importing its business.
package streamfixture

import (
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture/bindings"
)

func Identity() rpc.Identity {
	return rpc.Identity{ID: "streamfixture/program", Version: "1", Protocol: rpc.Protocol}
}
func Boundary(importOnly bool) rpc.Boundary {
	b := rpc.Boundary{Imports: []rpc.Binding{bindings.Callback}}
	if !importOnly {
		b.Exports = []rpc.Binding{bindings.Generator, bindings.Inspector}
	}
	return b
}
