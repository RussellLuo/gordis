package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"example.com/gordis-build-custom/buildconfig"
	builder "github.com/RussellLuo/gordis/build"
)

func main() {
	output := flag.String("output", "custom-greeting", "new output binary path")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "positional arguments are not supported; run from the custom module directory")
		os.Exit(2)
	}

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	request, adapter := buildconfig.Recipe(dir, *output)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, err := builder.Build(ctx, request, adapter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Built %s\nBuild record: %s\n", result.BinaryPath, result.RecordPath)
}
