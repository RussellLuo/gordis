package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"example.com/gordis-application-plugin/internal/appplugin"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

// The build injects the version; each installed instance runs this binary in
// its own process. The Host never imports this package.
var version = "1.1.0"

type samplerPlugin struct {
	Label string `json:"label"`
}

func (*samplerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       appplugin.BackendPluginID,
		Provides: []gordis.ServiceSpec{appplugin.EndpointsKey.Spec()},
		New:      func() gordis.Plugin { return new(samplerPlugin) },
	}
}

func (p *samplerPlugin) Validate() error {
	if p.Label == "" {
		return fmt.Errorf("label required")
	}
	return nil
}

func (p *samplerPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sample", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Sequence int `json:"sequence"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil || input.Sequence < 1 {
			http.Error(w, "positive sequence required", http.StatusBadRequest)
			return
		}
		base := 1000
		if version == "2.1.0" {
			base = 2000
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": version, "label": p.Label, "instance": scope.ID(),
			"generation": scope.Generation(), "sequence": input.Sequence,
			"value": base + input.Sequence, "pid": os.Getpid(),
		})
	})
	endpoint, err := appplugin.StartHTTP(scope, "api", mux)
	if err != nil {
		return err
	}
	return appplugin.Provide(scope, endpoint)
}

func main() {
	err := processbridge.Serve(context.Background(), os.Stdin, os.Stdout, processbridge.ServeOptions{
		Identity: process.Identity{
			Protocol: process.Protocol,
			Package:  "sampler",
			Version:  version,
		},
		Plugin:   new(samplerPlugin),
		Provides: []processbridge.Binding{appplugin.EndpointsBinding},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
