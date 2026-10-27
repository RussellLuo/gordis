// Package app owns greeting defaults, runtime flags and the Host lifecycle.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"time"

	"example.com/gordis-minimal/plugins/hello"
	"github.com/RussellLuo/gordis"
)

// Run implements the native build contract. --check registers all compiled
// types without mounting an instance; factories and metadata must remain pure.
func Run(argv []string, plugins []gordis.Plugin) (err error) {
	flags := flag.NewFlagSet("hello", flag.ContinueOnError)
	check := flags.Bool("check", false, "validate compiled plugins without activation")
	plugin := flags.String("plugin", "greet/hello", "greeting plugin type")
	name := flags.String("name", "World", "person to greet")
	if err := flags.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected application arguments: %v", flags.Args())
	}

	plugins = append(hello.Plugins(), plugins...)
	host, err := gordis.NewHost(plugins...)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, host.Shutdown(cleanup))
	}()
	if *check {
		return nil
	}

	config, err := json.Marshal(map[string]string{"name": *name})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID:     "greeting",
		Plugin: *plugin,
		Config: config,
	})
	return err
}
