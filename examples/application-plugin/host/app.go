package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"example.com/gordis-application-plugin/internal/appplugin"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

type backendConfig struct {
	Directory string          `json:"directory"`
	Digest    string          `json:"digest"`
	Config    json.RawMessage `json:"config"`
}
type instanceView struct {
	gordis.Snapshot
	PackageID string `json:"packageID"`
	Title     string `json:"title"`
	PID       int    `json:"pid"`
}
type catalog struct {
	Instances    []instanceView            `json:"instances"`
	Packages     []uiPackage               `json:"packages"`
	Available    []availablePackage        `json:"available"`
	Operation    *gordis.OperationSnapshot `json:"operation,omitempty"`
	HostPID      int                       `json:"hostPID"`
	HostIdentity string                    `json:"hostIdentity"`
}
type app struct {
	host                   *gordis.Host
	base, assets, packages string
	mu                     sync.Mutex
	control                sync.Mutex
	entries                map[string]*endpoint
	// Retain immutable browser files for old in-flight imports until host exit.
	bundles       map[string]*bundle
	clients       []*process.Client
	lastOperation uint64
}

func (a *app) resolveBackend(raw json.RawMessage) (process.Options, error) {
	var config backendConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return process.Options{}, err
	}
	b, err := loadBundle(config.Directory)
	if err != nil {
		return process.Options{}, err
	}
	if b.digest != config.Digest {
		return process.Options{}, fmt.Errorf("bundle changed")
	}
	return process.Options{
		Path:     b.backend.Executable,
		Identity: b.backend.Identity(),
		Config:   append(json.RawMessage(nil), config.Config...),
	}, nil
}

func newApp(base, assets, packages string) (*app, error) {
	if !regexp.MustCompile(`^/[A-Za-z0-9/_-]*$`).MatchString(base) || strings.Contains(base, "//") {
		return nil, fmt.Errorf("invalid base path")
	}
	base = strings.TrimRight(base, "/") + "/"
	a := &app{
		base:     base,
		assets:   assets,
		packages: packages,
		entries:  map[string]*endpoint{},
		bundles:  map[string]*bundle{},
	}
	backend, err := (processbridge.Adapter{
		ID:       appplugin.BackendPluginID,
		Provides: []processbridge.Binding{appplugin.EndpointsBinding},
		Resolve:  a.resolveBackend,
		OnClient: func(client *process.Client) {
			a.mu.Lock()
			a.clients = append(a.clients, client)
			a.mu.Unlock()
		},
	}).Plugin()
	if err != nil {
		return nil, err
	}
	a.host, err = gordis.NewHost([]gordis.Plugin{backend, &gatewayPlugin{app: a}}, nil)
	if err != nil {
		return nil, err
	}
	if err = a.host.Start(context.Background()); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *app) catalog() catalog {
	out := catalog{
		Instances:    []instanceView{},
		Packages:     []uiPackage{},
		Available:    []availablePackage{},
		HostPID:      os.Getpid(),
		HostIdentity: fmt.Sprintf("%p", a.host),
	}
	seen := map[string]bool{}
	snapshots := a.host.Snapshot()
	diagnostics := a.diagnostics()
	a.mu.Lock()
	for _, s := range snapshots {
		if s.Plugin != appplugin.BackendPluginID {
			continue
		}
		packageID, localID, _ := strings.Cut(s.ID, "--")
		v := instanceView{Snapshot: s, PackageID: packageID, Title: localID}
		for _, b := range a.bundles {
			if b.manifest.ID == packageID && b.manifest.Version == s.Version {
				for _, instance := range b.manifest.Instances {
					if instance.ID == localID {
						v.Title = instance.Title
					}
				}
			}
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Instance == s.ID && diagnostic.Generation == s.Generation {
				v.PID = diagnostic.PID
			}
		}
		if e := a.entries[s.ID]; e != nil && s.State == gordis.Ready &&
			s.GroupReady && s.Generation == e.generation {
			if !seen[e.bundle.digest] {
				out.Packages = append(out.Packages, e.bundle.view(a.base))
				seen[e.bundle.digest] = true
			}
		}
		out.Instances = append(out.Instances, v)
	}
	id := a.lastOperation
	a.mu.Unlock()
	if id != 0 {
		if o, ok := a.host.Operation(id); ok {
			out.Operation = &o
		}
	}
	// Discover packages built after startup without importing any plugin code.
	dirs, _ := os.ReadDir(a.packages)
	for _, d := range dirs {
		if !d.IsDir() || !packageName.MatchString(d.Name()) {
			continue
		}
		available := availablePackage{ID: d.Name(), Title: d.Name(), Versions: []string{}}
		versions, _ := os.ReadDir(filepath.Join(a.packages, d.Name()))
		for _, v := range versions {
			if !v.IsDir() || !versionName.MatchString(v.Name()) {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(a.packages, d.Name(), v.Name(), "bundle.json"))
			var m bundleManifest
			if err == nil && len(raw) <= 64*1024 && json.Unmarshal(raw, &m) == nil && m.ID == d.Name() && m.Version == v.Name() {
				available.Title = m.UI.Title
				available.Versions = append(available.Versions, v.Name())
			}
		}
		if len(available.Versions) > 0 {
			out.Available = append(out.Available, available)
		}
	}
	return out
}

type availablePackage struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Versions []string `json:"versions"`
}

var versionName = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var packageName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

func (a *app) install(ctx context.Context, packageID, version string) (uint64, error) {
	if !packageName.MatchString(packageID) || !versionName.MatchString(version) {
		return 0, fmt.Errorf("invalid package or version")
	}
	directory, err := filepath.Abs(filepath.Join(a.packages, packageID, version))
	if err != nil {
		return 0, err
	}
	b, err := loadBundle(directory)
	if err != nil {
		return 0, err
	}
	if b.manifest.ID != packageID || b.manifest.Version != version {
		return 0, fmt.Errorf("package directory/identity mismatch")
	}
	disabled := map[string]bool{}
	for _, s := range a.host.Snapshot() {
		if s.Plugin == appplugin.BackendPluginID {
			disabled[s.ID] = !s.DesiredEnabled
		}
	}
	change := gordis.Change{}
	next := map[string]bool{}
	for _, instance := range b.manifest.Instances {
		id := packageID + "--" + instance.ID
		gatewayID := id + "--gateway"
		slot := appplugin.EndpointContract + "/" + id
		next[id], next[gatewayID] = true, true
		config, _ := json.Marshal(backendConfig{Directory: directory, Digest: b.digest, Config: instance.Config})
		change.Upsert = append(change.Upsert, gordis.InstanceSpec{
			ID:       id,
			Plugin:   appplugin.BackendPluginID,
			Version:  version,
			Disabled: disabled[id],
			Config:   config,
			Outputs:  map[string]string{appplugin.EndpointContract: slot},
		})
		gateway, _ := json.Marshal(gatewayPlugin{
			BackendID: id,
			Directory: directory,
			Digest:    b.digest,
		})
		change.Upsert = append(change.Upsert, gordis.InstanceSpec{
			ID:      gatewayID,
			Plugin:  appplugin.GatewayPluginID,
			Version: version,
			Parent:  id,
			Config:  gateway,
			Inputs:  map[string]string{appplugin.EndpointContract: slot},
		})
	}
	for _, s := range a.host.Snapshot() {
		if strings.HasPrefix(s.ID, packageID+"--") && !next[s.ID] {
			change.Remove = append(change.Remove, s.ID)
		}
	}
	plan, err := a.host.Preview(change)
	if err != nil {
		return 0, err
	}
	a.mu.Lock()
	a.bundles[b.digest] = b
	a.mu.Unlock()
	return a.apply(ctx, plan)
}

func (a *app) apply(ctx context.Context, plan *gordis.Plan) (uint64, error) {
	id, err := a.host.Apply(ctx, plan)
	if id != 0 {
		a.mu.Lock()
		a.lastOperation = id
		a.mu.Unlock()
	}
	return id, err
}

func (a *app) diagnostics() []process.Diagnostics {
	a.mu.Lock()
	clients := append([]*process.Client{}, a.clients...)
	a.mu.Unlock()
	out := []process.Diagnostics{}
	for _, c := range clients {
		out = append(out, c.Snapshot())
	}
	return out
}
