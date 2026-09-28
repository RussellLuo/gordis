// This command is only the process entrypoint. The Plugin implementation
// and processbridge configuration live in importable packages.
package main

import (
	"context"
	"fmt"
	"os"

	greeterproc "github.com/RussellLuo/gordis/examples/process/greeter/proc"
)

func main() {
	if err := greeterproc.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
