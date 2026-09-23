package main

import (
	"context"
	"fmt"
	"net/http"

	"example.com/gordis-application-plugin/internal/appplugin"
	"github.com/RussellLuo/gordis"
)

type gatewayPlugin struct {
	BackendID string `json:"backendID"`
	Directory string `json:"directory"`
	Digest    string `json:"digest"`
	app       *app
}

type endpoint struct {
	scope      *gordis.Scope
	generation uint64
	bundle     *bundle
	proxy      http.Handler
}

func (p *gatewayPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       appplugin.GatewayPluginID,
		Requires: []gordis.ServiceSpec{appplugin.EndpointsKey.Spec()},
		New:      func() gordis.Plugin { return &gatewayPlugin{app: p.app} },
	}
}

func (p *gatewayPlugin) Validate() error {
	if p.BackendID == "" {
		return fmt.Errorf("backend ID required")
	}
	b, err := loadBundle(p.Directory)
	if err == nil && b.digest != p.Digest {
		err = fmt.Errorf("bundle changed")
	}
	return err
}

func (p *gatewayPlugin) Start(ctx context.Context, scope *gordis.Scope) error {
	// Re-read and revalidate the package at activation. The Gateway is generic:
	// application-specific behavior stays behind the endpoint contract.
	b, err := loadBundle(p.Directory)
	if err != nil {
		return err
	}
	if b.digest != p.Digest {
		return fmt.Errorf("bundle changed")
	}
	service, err := gordis.Get(scope, appplugin.EndpointsKey)
	if err != nil {
		return err
	}
	endpoints, err := service.List(ctx)
	if err != nil {
		return err
	}
	var remote appplugin.Endpoint
	for _, candidate := range endpoints {
		if candidate.Name == "api" {
			if remote.Name != "" {
				return fmt.Errorf("application provides duplicate api endpoints")
			}
			remote = candidate
		}
	}
	if remote.Name == "" {
		return fmt.Errorf("application must provide an api endpoint")
	}
	if remote.Instance != p.BackendID {
		return fmt.Errorf("endpoint belongs to unexpected backend")
	}
	proxy, closeIdle, err := newProxy(remote)
	if err != nil {
		return err
	}
	if err = scope.Defer("HTTP connections", func(context.Context) error { closeIdle(); return nil }); err != nil {
		closeIdle()
		return err
	}
	e := &endpoint{scope: scope, generation: remote.Generation, bundle: b, proxy: proxy}
	p.app.mu.Lock()
	p.app.entries[p.BackendID] = e
	p.app.bundles[b.digest] = b
	p.app.mu.Unlock()
	remove := func(context.Context) error {
		p.app.mu.Lock()
		defer p.app.mu.Unlock()
		if p.app.entries[p.BackendID] == e {
			delete(p.app.entries, p.BackendID)
		}
		return nil
	}
	if err = scope.Defer("HTTP and UI registration", remove); err != nil {
		_ = remove(ctx)
		return err
	}
	if err = scope.OnStop("HTTP and UI entrance", remove); err != nil {
		_ = remove(ctx)
		return err
	}
	return nil
}

var _ gordis.Plugin = (*gatewayPlugin)(nil)
var _ gordis.Validator = (*gatewayPlugin)(nil)
