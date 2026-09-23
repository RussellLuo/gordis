package contract

import (
	"context"

	"github.com/RussellLuo/gordis"
)

const (
	Package = "example-greeter"
	Version = "1.0.0"
)

type Greeter interface {
	Greet(context.Context, string) (string, error)
}

var GreeterKey = gordis.NewKey[Greeter]("example.process.greeter/1")
