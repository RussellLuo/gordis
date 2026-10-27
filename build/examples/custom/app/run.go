package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/RussellLuo/gordis"
)

// Run uses the selected registration's default unless --name is explicit.
// --verify-assembly returns after static checks, before any activation.
func Run(argv []string, application *Application) (err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, application.host.Shutdown(cleanup))
	}()
	flags := flag.NewFlagSet("custom-greeting", flag.ContinueOnError)
	verify := flags.Bool("verify-assembly", false, "check assembly without activating plugins")
	plugin := flags.String("plugin", "greet/hello", "greeting plugin type")
	name := flags.String("name", "", "override the registration's default name")
	if err := flags.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected application arguments: %v", flags.Args())
	}
	if *verify {
		return nil
	}

	defaultName, ok := application.defaults[*plugin]
	if !ok {
		return fmt.Errorf("unknown greeting plugin %q", *plugin)
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "name" {
			defaultName = *name
		}
	})
	config, err := json.Marshal(map[string]string{"name": defaultName})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = application.host.Env().MountReady(ctx, gordis.InstanceSpec{
		ID:     "greeting",
		Plugin: *plugin,
		Config: config,
	})
	return err
}
