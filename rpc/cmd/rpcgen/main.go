// Command rpcgen generates a companion Binding package from a Go interface.
package main

import (
	"fmt"
	"os"

	"github.com/RussellLuo/gordis/rpc/internal/rpcgen"
)

func main() {
	if err := rpcgen.Run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "rpcgen:", err)
		os.Exit(1)
	}
}
