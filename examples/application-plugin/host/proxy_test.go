package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/gordis-application-plugin/internal/appplugin"
	"github.com/RussellLuo/gordis"
)

type proxyTestPlugin struct {
	app       *app
	proxy     http.Handler
	withdrawn chan struct{}
}

func (p *proxyTestPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test", New: func() gordis.Plugin {
		return &proxyTestPlugin{app: p.app, proxy: p.proxy, withdrawn: p.withdrawn}
	}}
}

func (p *proxyTestPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	p.app.mu.Lock()
	p.app.entries[scope.ID()] = &endpoint{scope: scope, generation: scope.Generation(), proxy: p.proxy}
	p.app.mu.Unlock()
	return scope.OnStop("HTTP entrance", func(context.Context) error {
		p.app.mu.Lock()
		delete(p.app.entries, scope.ID())
		p.app.mu.Unlock()
		close(p.withdrawn)
		return nil
	})
}

func TestProxyPreservesHTTPAndReplacesPrivateCredentials(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := bytes.Repeat([]byte{0, 1, 255}, 30000)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.RequestURI != "/bytes/a%2Fb?x=1&x=2&encoded=%2F" {
			t.Errorf("request: %s %s", r.Method, r.RequestURI)
		}
		if r.Header.Get(appplugin.TokenHeader) != token || r.Header.Get("X-Gordis-Generation") != "" {
			t.Error("credentials not replaced")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Add("X-Multi", "one")
		w.Header().Add("X-Multi", "two")
		w.Header().Set(appplugin.TokenHeader, "must-not-leak")
		w.WriteHeader(201)
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	proxy, closeIdle, err := newProxy(appplugin.Endpoint{
		Name: "api", Protocol: appplugin.HTTPProtocol, URL: upstream.URL, Token: token,
		Instance: "sampler--alpha", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeIdle()
	r := httptest.NewRequest("PUT", "/bytes/a%2Fb?x=1&x=2&encoded=%2F", bytes.NewReader(body))
	r.Header.Set(appplugin.TokenHeader, "browser-forgery")
	r.Header.Set("X-Gordis-Generation", "1")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	if w.Code != 201 ||
		!bytes.Equal(w.Body.Bytes(), body) ||
		strings.Join(w.Header().Values("X-Multi"), ",") != "one,two" ||
		w.Header().Get(appplugin.TokenHeader) != "" {
		t.Fatalf(
			"response: status=%d size=%d header=%v body=%q",
			w.Code, w.Body.Len(), w.Header(), w.Body.Bytes()[:min(200, w.Body.Len())],
		)
	}
}

func TestStreamingLeaseDrainsOnClientDisconnect(t *testing.T) {
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ready\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()
	proxy, closeIdle, err := newProxy(appplugin.Endpoint{
		Name: "api", Protocol: appplugin.HTTPProtocol, URL: upstream.URL,
		Token:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Instance: "sampler--alpha", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeIdle()
	a := &app{base: "/nested/demo/", entries: map[string]*endpoint{}, managed: map[string]*managedInstance{}}
	withdrawn := make(chan struct{})
	a.host, err = gordis.NewHost([]gordis.Plugin{&proxyTestPlugin{app: a, proxy: proxy, withdrawn: withdrawn}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := a.host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "sampler--alpha", Plugin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.host.Shutdown(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc(a.base+"api/extensions/{id}/{rest...}", a.forward)
	front := httptest.NewServer(mux)
	defer front.Close()
	address := front.URL + a.base + "api/extensions/sampler--alpha/events"
	for _, generation := range []string{"", "0", "2"} {
		r, _ := http.NewRequest("GET", address, nil)
		r.Header.Set("X-Gordis-Generation", generation)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 409 {
			t.Fatalf("stale request: %d", res.StatusCode)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", address, nil)
	r.Header.Set("X-Gordis-Generation", "1")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || line != "data: ready\n" {
		t.Fatal(line, err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- instance.Unmount(context.Background()) }()
	select {
	case <-withdrawn:
	case <-time.After(3 * time.Second):
		t.Fatal("entrance was not withdrawn")
	}
	select {
	case err := <-stopped:
		t.Fatalf("stop overtook live stream: %v", err)
	default:
	}
	blocked, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	blocked.Body.Close()
	if blocked.StatusCode != 409 {
		t.Fatal("stopping instance accepted new request")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not canceled")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lease not released")
	}
}
