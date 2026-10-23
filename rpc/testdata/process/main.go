package main

import (
	"context"
	"fmt"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture"
	"github.com/RussellLuo/gordis/rpc/internal/processfixture/assembly"
	"os"
)

func main() {
	if err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: processfixture.Identity(), Unit: assembly.Unit(os.Args[1])}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
