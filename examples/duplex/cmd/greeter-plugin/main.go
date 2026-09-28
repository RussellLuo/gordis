// This command only starts the process transport. The business Plugin and its
// processbridge bindings live in importable packages.
package main

import (
	"context"
	"fmt"
	"os"

	greeterproc "github.com/RussellLuo/gordis/examples/duplex/greeter/proc"
)

func main() {
	if err := greeterproc.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
