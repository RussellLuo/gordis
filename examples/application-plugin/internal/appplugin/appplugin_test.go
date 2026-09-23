package appplugin

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
)

func TestEndpointRestrictedToPrivateLoopbackListener(t *testing.T) {
	valid := Endpoint{
		Name: "api", Protocol: HTTPProtocol, URL: "http://127.0.0.1:23456",
		Token:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Instance: "sampler--alpha", Generation: 1,
	}
	for _, address := range []string{
		"http://example.com:80", "http://localhost:80", "http://127.0.0.1:0", "http://127.0.0.1:65536",
		"https://127.0.0.1:80", "http://user@127.0.0.1:80", "http://127.0.0.1:80/path",
		"http://127.0.0.1:80?token=secret", "http://127.0.0.1:80?", "http://127.0.0.1:80#fragment",
	} {
		endpoint := valid
		endpoint.URL = address
		if _, err := ParseHTTP(endpoint); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	if _, err := ParseHTTP(valid); err != nil {
		t.Fatal(err)
	}
}

type httpFixture struct {
	endpoint  chan<- Endpoint
	entered   chan struct{}
	completed chan struct{}
	release   <-chan struct{}
}

func (p *httpFixture) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "http-fixture", New: func() gordis.Plugin {
		return &httpFixture{endpoint: p.endpoint, entered: p.entered, completed: p.completed, release: p.release}
	}}
}

func (p *httpFixture) Start(_ context.Context, scope *gordis.Scope) error {
	endpoint, err := StartHTTP(scope, "api", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(p.entered)
		<-p.release
		_, _ = io.WriteString(w, "done")
		close(p.completed)
	}))
	if err == nil {
		p.endpoint <- endpoint
	}
	return err
}

func TestHTTPDataPlaneDrainsRemoteScopeLease(t *testing.T) {
	endpoints := make(chan Endpoint, 1)
	entered := make(chan struct{})
	completed := make(chan struct{})
	release := make(chan struct{})
	plugin := &httpFixture{endpoint: endpoints, entered: entered, completed: completed, release: release}
	host, err := gordis.NewHost(
		[]gordis.Plugin{plugin},
		[]gordis.InstanceSpec{{ID: "application", Plugin: "http-fixture"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer host.Stop(context.Background())
	endpoint := <-endpoints
	request, _ := http.NewRequest(http.MethodGet, endpoint.URL+"/stream", nil)
	request.Header.Set(TokenHeader, endpoint.Token)
	response := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(request)
		if err == nil {
			_, err = io.ReadAll(res.Body)
			res.Body.Close()
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP handler did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- host.StopInstance(context.Background(), "application") }()
	select {
	case err := <-stopped:
		t.Fatalf("stop overtook active HTTP request: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP handler did not finish")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Scope lease did not drain")
	}
	select {
	case <-response:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP client did not return")
	}
}
