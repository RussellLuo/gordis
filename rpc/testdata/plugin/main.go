package main

import (
	"context"
	"fmt"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/testfixture"
	"os"
)

func main() {
	if err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: testfixture.Identity(), Unit: testfixture.Unit()}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
