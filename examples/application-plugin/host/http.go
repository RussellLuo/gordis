package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RussellLuo/gordis"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func failure(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (a *app) handler() (http.Handler, error) {
	raw, err := os.ReadFile(filepath.Join(a.assets, "build.json"))
	if err != nil {
		return nil, fmt.Errorf("build host UI first: %w", err)
	}
	var build struct {
		Imports map[string]string `json:"imports"`
	}
	if err = json.Unmarshal(raw, &build); err != nil {
		return nil, err
	}
	for key, file := range build.Imports {
		if !fs.ValidPath(file) || strings.ContainsAny(file, "?#\\") {
			return nil, fmt.Errorf("invalid shared asset")
		}
		if _, err = os.Stat(filepath.Join(a.assets, file)); err != nil {
			return nil, err
		}
		build.Imports[key] = a.base + "assets/" + file
	}
	imports, _ := json.Marshal(map[string]any{"imports": build.Imports})
	index, err := os.ReadFile(filepath.Join(a.assets, "index.html"))
	if err != nil {
		return nil, err
	}
	html := strings.ReplaceAll(strings.ReplaceAll(string(index), "__BASE__", a.base), "__IMPORT_MAP__", string(imports))
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+a.base+"api/plugins", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, a.catalog())
	})
	mux.HandleFunc("GET "+a.base+"api/processes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, a.diagnostics())
	})
	mux.HandleFunc("POST "+a.base+"api/package/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !a.control.TryLock() {
			failure(w, 409, gordis.ErrBusy)
			return
		}
		defer a.control.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var id uint64
		var err error
		var body struct {
			PackageID string `json:"packageID"`
			Version   string `json:"version"`
		}
		reader := http.MaxBytesReader(w, r.Body, 4096)
		if err = json.NewDecoder(reader).Decode(&body); err != nil ||
			!packageName.MatchString(body.PackageID) {
			failure(w, 400, fmt.Errorf("valid packageID required"))
			return
		}
		switch r.PathValue("action") {
		case "load":
			id, err = a.install(ctx, body.PackageID, body.Version)
		case "uninstall":
			id, err = a.uninstall(ctx, body.PackageID)
		default:
			failure(w, 404, fmt.Errorf("unknown package action"))
			return
		}
		if err != nil {
			writeJSON(w, 409, map[string]any{"operation": id, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"operation": id})
	})
	mux.HandleFunc("POST "+a.base+"api/plugins/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !a.control.TryLock() {
			failure(w, 409, gordis.ErrBusy)
			return
		}
		defer a.control.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var err error
		var id uint64
		switch r.PathValue("action") {
		case "enable":
			id, err = a.setEnabled(ctx, r.PathValue("id"), true)
		case "disable":
			id, err = a.setEnabled(ctx, r.PathValue("id"), false)
		default:
			failure(w, 404, fmt.Errorf("unknown instance action"))
			return
		}
		if err != nil {
			failure(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "operation": id})
	})
	mux.HandleFunc(a.base+"api/extensions/{id}/{rest...}", a.forward)
	assetRoute := "GET " + a.base + "assets/plugins/{package}/{digest}/{file...}"
	mux.HandleFunc(assetRoute, func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		b := a.bundles[r.PathValue("digest")]
		a.mu.Unlock()
		if b == nil || b.manifest.ID != r.PathValue("package") {
			http.NotFound(w, r)
			return
		}
		data, ok := b.files[r.PathValue("file")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, r.PathValue("file"), time.Time{}, bytes.NewReader(data))
	})
	mux.Handle("GET "+a.base+"assets/", http.StripPrefix(a.base+"assets/", http.FileServer(http.Dir(a.assets))))
	shell := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(html))
	}
	mux.HandleFunc("GET "+a.base+"{$}", shell)
	mux.HandleFunc("GET "+a.base+"extensions/{rest...}", shell)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					failure(w, 403, fmt.Errorf("origin rejected"))
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	}), nil
}
