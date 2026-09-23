package main

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"strings"

	"example.com/gordis-application-plugin/internal/appplugin"
	"github.com/RussellLuo/gordis"
)

func newProxy(endpoint appplugin.Endpoint) (http.Handler, func(), error) {
	target, err := appplugin.ParseHTTP(endpoint)
	if err != nil {
		return nil, nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Never send the process token through an environment proxy.
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			r.Out.Header.Set(appplugin.TokenHeader, endpoint.Token)
			r.Out.Header.Del("X-Gordis-Generation")
			// SetURL preserves RawPath. Preserve the opaque query, including repeats.
			r.Out.URL.RawQuery = r.In.URL.RawQuery
		},
		ModifyResponse: func(r *http.Response) error {
			r.Header.Del(appplugin.TokenHeader)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			failure(w, http.StatusBadGateway, fmt.Errorf("plugin HTTP endpoint unavailable"))
		},
	}
	return proxy, transport.CloseIdleConnections, nil
}

// The route exists once. Its target exists only for the current ready Scope.
// The lease lasts until the complete body (including SSE) has been forwarded.
func (a *app) forward(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "" {
		failure(w, 400, fmt.Errorf("connection upgrades are not supported by this example"))
		return
	}
	id := r.PathValue("id")
	a.mu.Lock()
	e := a.entries[id]
	a.mu.Unlock()
	if e == nil {
		failure(w, 409, fmt.Errorf("instance unavailable"))
		return
	}
	ready := false
	for _, s := range a.host.Snapshot() {
		if s.ID == id && s.State == gordis.Ready && s.Generation == e.generation {
			ready = true
		}
	}
	if !ready {
		failure(w, 409, fmt.Errorf("instance not ready"))
		return
	}
	if r.Header.Get("X-Gordis-Generation") != fmt.Sprint(e.generation) {
		failure(w, 409, fmt.Errorf("instance generation changed; refresh the UI"))
		return
	}
	release, err := e.scope.Acquire()
	if err != nil {
		failure(w, 409, err)
		return
	}
	defer release()
	// Reject encoded IDs; this prefix must strip both Path and RawPath exactly.
	prefix := a.base + "api/extensions/" + id
	if !strings.HasPrefix(r.URL.EscapedPath(), prefix+"/") {
		http.NotFound(w, r)
		return
	}
	http.StripPrefix(prefix, e.proxy).ServeHTTP(w, r)
}
