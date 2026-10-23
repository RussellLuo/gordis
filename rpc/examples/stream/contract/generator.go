// Package contract defines the ordinary streaming API shared by both programs.
package contract

import (
	"context"

	"github.com/RussellLuo/gordis"
)

//go:generate go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Generator -key GeneratorKey -version Version -package bindings -out ../bindings/generator.go

const GeneratorPluginID = "example/stream/generator-provider"
const Version = "1"

type Chunk struct {
	Index int `json:"index"`
}

type Generator interface {
	// Generate yields 1 through count. Zero is empty; a negative count is invalid.
	// It returns immediately on context cancellation or a yield error.
	Generate(context.Context, int, func(Chunk) error) error
}

var GeneratorKey = gordis.NewKey[Generator]("example/stream/generator")
