// This example keeps one reader Ready while an optional cache appears and
// disappears. Each request uses Lookup to choose the cache or a required database.
//
// Run from the repository root: go run ./examples/optional-service
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/RussellLuo/gordis"
)

type ValueStore interface {
	Get(context.Context, string) ([]byte, error)
}

var databaseKey = gordis.NewKey[ValueStore]("example.database/1")
var cacheKey = gordis.NewKey[ValueStore]("example.cache/1")

// Both providers use an immutable in-memory value to identify the chosen path.
type store string

func (s store) Get(_ context.Context, id string) ([]byte, error) {
	return []byte(fmt.Sprintf("%s:%s", s, id)), nil
}

type storePlugin struct {
	id  string
	key gordis.Key[ValueStore]
}

func (p *storePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       p.id,
		Provides: []gordis.ServiceSpec{p.key.Spec()},
		New:      func() gordis.Plugin { return &storePlugin{id: p.id, key: p.key} },
	}
}

func (p *storePlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide[ValueStore](p.key, store(p.id))
}

type readerPlugin struct {
	readerCh    chan<- *readerPlugin
	generation  uint64 // Used only in output.
	database    ValueStore
	lookupCache func() (ValueStore, bool, error)
}

func (p *readerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "reader",
		Requires: []gordis.ServiceSpec{databaseKey.Spec()},
		New:      func() gordis.Plugin { return &readerPlugin{readerCh: p.readerCh} },
	}
}

func (p *readerPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	database, err := scope.Get(databaseKey)
	if err != nil {
		return err
	}
	p.generation, p.database = scope.Generation(), database
	// Query the cache on each request using this Scope.
	p.lookupCache = func() (ValueStore, bool, error) {
		return scope.Lookup(cacheKey)
	}
	p.readerCh <- p
	return nil
}

func (p *readerPlugin) Get(ctx context.Context, id string) ([]byte, error) {
	cache, found, err := p.lookupCache()
	if err != nil {
		return nil, err
	}
	if found {
		return cache.Get(ctx, id)
	}
	return p.database.Get(ctx, id)
}

func run() error {
	// The buffer lets Activate hand off the reader before MountReady returns.
	readerCh := make(chan *readerPlugin, 1)
	host, err := gordis.NewHost(
		&storePlugin{id: "database", key: databaseKey},
		&storePlugin{id: "cache", key: cacheKey},
		&readerPlugin{readerCh: readerCh},
	)
	if err != nil {
		return err
	}
	defer host.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "database", Plugin: "database"}); err != nil {
		return err
	}
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "reader", Plugin: "reader"}); err != nil {
		return err
	}
	// MountReady guarantees readiness; the channel only transfers the reader.
	reader := <-readerCh
	request := func() error {
		value, err := reader.Get(ctx, "item")
		if err != nil {
			return err
		}
		fmt.Printf("reader generation %d: %s\n", reader.generation, value)
		return nil
	}

	// No cache is mounted: the request uses the required database.
	if err := request(); err != nil {
		return err
	}

	// Mount the cache: the next request uses it without restarting the reader.
	cache, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "cache", Plugin: "cache"})
	if err != nil {
		return err
	}
	if err := request(); err != nil {
		return err
	}

	// Unmount the cache: the request falls back to the database again.
	if err := cache.Unmount(ctx); err != nil {
		return err
	}
	return request()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
