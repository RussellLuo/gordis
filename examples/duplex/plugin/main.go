// The plugin handles one method and calls the Host while that request is open.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis/examples/duplex/contract"
	"github.com/RussellLuo/gordis/process"
)

func main() {
	err := process.Serve(context.Background(), os.Stdin, os.Stdout,
		contract.Identity, process.Limits{}, process.Handler{
			Call: func(ctx context.Context, host process.Caller, method string, raw json.RawMessage) (any, error) {
				if method != contract.AddMethod {
					return nil, &process.RPCError{Code: "method", Message: "unknown plugin method"}
				}
				var number int
				if err := json.Unmarshal(raw, &number); err != nil {
					return nil, err
				}
				var base int
				if err := host.Call(ctx, contract.BaseMethod, nil, &base); err != nil {
					return nil, err
				}
				return number + base, nil
			},
		})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
