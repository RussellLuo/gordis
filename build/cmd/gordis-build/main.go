package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	builder "github.com/RussellLuo/gordis/build"
	"github.com/RussellLuo/gordis/build/native"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := builder.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, native.Command())
	cancel()
	os.Exit(code)
}
