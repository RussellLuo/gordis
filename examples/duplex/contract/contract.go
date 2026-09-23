// Package contract contains the wire identity and method names shared by the
// two independently built programs in this example.
package contract

import "github.com/RussellLuo/gordis/process"

const (
	AddMethod  = "add"
	BaseMethod = "host.base"
)

var Identity = process.Identity{
	Protocol:  process.Protocol,
	Package:   "duplex",
	Version:   "1.0.0",
	Contracts: []string{"example.duplex/1"},
}
