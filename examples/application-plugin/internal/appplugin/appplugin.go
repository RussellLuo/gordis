// Package appplugin defines the stable contract implemented by every
// independently delivered application backend in this example.
package appplugin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

const (
	BackendPluginID  = "application-backend"
	GatewayPluginID  = "application-gateway"
	EndpointContract = "gordis.application.endpoints/1"
	HTTPProtocol     = "http"
	TokenHeader      = "X-Gordis-Process-Token"
)

// Endpoint describes one private data-plane listener owned by an application
// Plugin. The Host assigns its public route and never exposes Token to the UI.
type Endpoint struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	URL        string `json:"url"`
	Token      string `json:"token"`
	Instance   string `json:"instance"`
	Generation uint64 `json:"generation"`
}

// Endpoints is the one stable service contract shared by otherwise unrelated
// application types. An application backend exposes one or more named data planes.
type Endpoints interface {
	List(context.Context) ([]Endpoint, error)
}

var EndpointsKey = gordis.NewKey[Endpoints](EndpointContract)

type endpointClient struct{ caller process.Caller }

func (c endpointClient) List(ctx context.Context) ([]Endpoint, error) {
	var endpoints []Endpoint
	err := c.caller.Call(ctx, "list", nil, &endpoints)
	return endpoints, err
}

var EndpointsBinding = processbridge.Bind(
	EndpointsKey,
	func(c process.Caller) Endpoints { return endpointClient{caller: c} },
	func(value Endpoints) process.CallHandler {
		return func(ctx context.Context, _ process.Caller, method string, _ json.RawMessage) (any, error) {
			if method != "list" {
				return nil, &process.RPCError{Code: "method", Message: "unknown endpoint method"}
			}
			return value.List(ctx)
		}
	},
)

type fixedEndpoints []Endpoint

func (e fixedEndpoints) List(context.Context) ([]Endpoint, error) {
	return append([]Endpoint(nil), e...), nil
}

func Provide(scope *gordis.Scope, endpoints ...Endpoint) error {
	if len(endpoints) == 0 {
		return errors.New("application plugin must provide at least one endpoint")
	}
	for _, endpoint := range endpoints {
		if endpoint.Instance != scope.ID() || endpoint.Generation != scope.Generation() {
			return errors.New("application endpoint belongs to another generation")
		}
		if err := Validate(endpoint); err != nil {
			return err
		}
	}
	return gordis.Provide(scope, EndpointsKey, Endpoints(fixedEndpoints(endpoints)))
}

// StartHTTP starts a private loopback data plane inside the Plugin's Scope.
// OnStop closes admission, request leases drain active handlers, and the final
// Defer closes remaining connections before processbridge reports disposal.
func StartHTTP(scope *gordis.Scope, name string, handler http.Handler) (Endpoint, error) {
	if name == "" || handler == nil {
		return Endpoint{}, errors.New("application HTTP endpoint needs a name and handler")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Endpoint{}, err
	}
	token := hex.EncodeToString(secret)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Endpoint{}, err
	}
	server := &http.Server{
		BaseContext: func(net.Listener) context.Context { return scope.Context() },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get(TokenHeader)), []byte(token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			r.Header.Del(TokenHeader)
			release, err := scope.Acquire()
			if err != nil {
				http.Error(w, "application endpoint stopping", http.StatusServiceUnavailable)
				return
			}
			defer release()
			handler.ServeHTTP(w, r)
		}),
	}
	closeListener := func(context.Context) error {
		server.SetKeepAlivesEnabled(false)
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
		return nil
	}
	if err := scope.Defer("application HTTP connections", func(context.Context) error {
		return server.Close()
	}); err != nil {
		_ = listener.Close()
		return Endpoint{}, err
	}
	if err := scope.OnStop("application HTTP listener", closeListener); err != nil {
		_ = listener.Close()
		return Endpoint{}, err
	}
	if err := scope.Go("application HTTP server", func(context.Context) error {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}); err != nil {
		_ = listener.Close()
		return Endpoint{}, err
	}
	return Endpoint{
		Name:       name,
		Protocol:   HTTPProtocol,
		URL:        "http://" + listener.Addr().String(),
		Token:      token,
		Instance:   scope.ID(),
		Generation: scope.Generation(),
	}, nil
}

func Validate(endpoint Endpoint) error {
	if endpoint.Name == "" || endpoint.Protocol == "" || endpoint.URL == "" || len(endpoint.Token) != 64 ||
		endpoint.Instance == "" || endpoint.Generation == 0 {
		return errors.New("invalid application endpoint")
	}
	return nil
}

// ParseHTTP accepts only the private address form emitted by StartHTTP.
func ParseHTTP(endpoint Endpoint) (*url.URL, error) {
	if err := Validate(endpoint); err != nil {
		return nil, err
	}
	if endpoint.Protocol != HTTPProtocol {
		return nil, errors.New("invalid application HTTP endpoint")
	}
	u, err := url.Parse(endpoint.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid application endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || u.Scheme != "http" ||
		u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("application endpoint must be http://127.0.0.1:port")
	}
	return u, nil
}

func RequiredContracts() []string {
	return []string{process.LifecycleContract, EndpointContract}
}
