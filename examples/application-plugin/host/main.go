package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dist := filepath.Dir(exe)

	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	base := flag.String("base", "/demo/", "deployment base path")
	assets := flag.String("assets", filepath.Join(dist, "web"), "host browser assets")
	packages := flag.String("packages", filepath.Join(dist, "packages"), "locally supplied versioned bundles")
	ready := flag.String("ready-file", "", "write URL after listening")
	flag.Parse()

	a, err := newApp(*base, *assets, *packages)
	if err != nil {
		return err
	}
	// Reclaim Host resources if a later setup or runtime step fails.
	defer a.host.Shutdown(context.Background())

	handler, err := a.handler()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	url := "http://" + listener.Addr().String() + a.base
	if *ready != "" {
		if err := os.WriteFile(*ready, []byte(url), 0600); err != nil {
			log.Print(err)
		}
	}
	fmt.Println(url)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
		}
	}

	// Freeze entrances and drain accepted requests before closing process pipes.
	if err := a.host.Shutdown(context.Background()); err != nil {
		log.Print(err)
	}
	shutdown, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if err := server.Shutdown(shutdown); err != nil {
		log.Print(err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
