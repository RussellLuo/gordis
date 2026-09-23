// The Host asks the plugin to add a number. While handling that request, the
// plugin calls back to the Host for the base value.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RussellLuo/gordis/examples/duplex/contract"
	"github.com/RussellLuo/gordis/process"
)

func run(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := process.Start(ctx, process.Options{
		Path: path, Identity: contract.Identity, Instance: "calculator", Generation: 1,
		Handler: func(_ context.Context, _ process.Caller, method string, _ json.RawMessage) (any, error) {
			if method != contract.BaseMethod {
				return nil, &process.RPCError{Code: "method", Message: "unknown Host method"}
			}
			fmt.Println("plugin → Host: base()")
			return 40, nil
		},
	})
	if err != nil {
		return err
	}

	fmt.Println("Host → plugin: add(2)")
	var result int
	callErr := client.Call(ctx, contract.AddMethod, 2, &result)
	closeErr := client.Close(context.Background())
	if err := errors.Join(callErr, closeErr); err != nil {
		return err
	}
	if result != 42 {
		return fmt.Errorf("got %d, want 42", result)
	}
	state := client.Snapshot()
	if !state.Reaped || !state.IOComplete || !state.HandlersComplete {
		return fmt.Errorf("plugin process was not fully reclaimed: %+v", state)
	}
	fmt.Printf("result: %d; plugin process reaped\n", result)
	return nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: duplex <plugin-executable>")
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}
