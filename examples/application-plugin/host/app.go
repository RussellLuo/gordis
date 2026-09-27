package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	ID        string `json:"id"`
	PackageID string `json:"packageID"`
	Title     string `json:"title"`
	Version   string `json:"version"`
	Mounted   bool   `json:"mounted"`
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
	managed                map[string]*managedInstance
	// Retain immutable browser files for old in-flight imports until host exit.
	bundles       map[string]*bundle
	clients       []*process.Client
	lastOperation *gordis.Operation
}

// managedInstance is Loader state, not Gordis runtime state. Package/version,
// enabled state, and retained Instance handles deliberately stay in the
// application control plane.
type managedInstance struct {
	id, packageID, title, version string
	backendConfig, gatewayConfig  json.RawMessage
	bundle                        *bundle
	backend                       *gordis.Instance
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
		managed:  map[string]*managedInstance{},
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
	a.host, err = gordis.NewHost([]gordis.Plugin{backend, &gatewayPlugin{app: a}})
	if err != nil {
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
	snapshots := map[string]gordis.Snapshot{}
	for _, snapshot := range a.host.Snapshot() {
		snapshots[snapshot.QualifiedID] = snapshot
	}
	diagnostics := a.diagnostics()
	a.mu.Lock()
	ids := make([]string, 0, len(a.managed))
	for id := range a.managed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		managed := a.managed[id]
		snapshot := snapshots[id]
		v := instanceView{
			Snapshot: snapshot, ID: id, PackageID: managed.packageID,
			Title: managed.title, Version: managed.version, Mounted: managed.backend != nil,
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Instance == id && diagnostic.Generation == snapshot.Generation {
				v.PID = diagnostic.PID
			}
		}
		if e := a.entries[id]; e != nil && snapshot.Phase == gordis.PhaseReady &&
			snapshot.Generation == e.generation {
			if !seen[e.bundle.digest] {
				out.Packages = append(out.Packages, e.bundle.view(a.base))
				seen[e.bundle.digest] = true
			}
		}
		out.Instances = append(out.Instances, v)
	}
	lastOperation := a.lastOperation
	a.mu.Unlock()
	if lastOperation != nil {
		snapshot := lastOperation.Snapshot()
		out.Operation = &snapshot
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
	a.mu.Lock()
	previous := map[string]*managedInstance{}
	for id, managed := range a.managed {
		if managed.packageID == packageID {
			previous[id] = managed
		}
	}
	a.mu.Unlock()

	next := map[string]*managedInstance{}
	for _, instance := range b.manifest.Instances {
		id := packageID + "--" + instance.ID
		config, _ := json.Marshal(backendConfig{Directory: directory, Digest: b.digest, Config: instance.Config})
		gateway, _ := json.Marshal(gatewayPlugin{
			BackendID: id,
			Directory: directory,
			Digest:    b.digest,
		})
		next[id] = &managedInstance{
			id: id, packageID: packageID, title: instance.Title, version: version,
			backendConfig: config, gatewayConfig: gateway, bundle: b,
		}
	}

	changes := a.host.Changes()
	requestCount := 0
	for _, managed := range previous {
		if managed.backend != nil {
			changes.Unmount(managed.backend)
			requestCount++
		}
	}
	mountOrder := []*managedInstance{}
	for _, instance := range b.manifest.Instances {
		id := packageID + "--" + instance.ID
		managed := next[id]
		old := previous[id]
		if old != nil && old.backend == nil {
			continue // Loader preserves an explicitly disabled entry across upgrades.
		}
		env := a.host.Env().Isolate(appplugin.EndpointsKey, id)
		changes.Mount(env, gordis.InstanceSpec{
			ID: id, Plugin: appplugin.BackendPluginID, Config: managed.backendConfig,
		})
		mountOrder = append(mountOrder, managed)
		requestCount++
	}
	var operation *gordis.Operation
	if requestCount != 0 {
		operation, err = a.startOperation(ctx, changes)
		if err != nil {
			return operationID(operation), err
		}
		mounted := operation.Mounted()
		if len(mounted) != len(mountOrder) {
			return operationID(operation), fmt.Errorf("mounted backend handle count changed")
		}
		for index := range mounted {
			mountOrder[index].backend = mounted[index]
		}
	}

	a.mu.Lock()
	a.bundles[b.digest] = b
	for id := range previous {
		delete(a.managed, id)
	}
	for id, managed := range next {
		a.managed[id] = managed
	}
	a.mu.Unlock()
	if operation != nil {
		if err := operation.WaitReady(ctx); err != nil {
			return operationID(operation), err
		}
	}
	gatewayOperation, err := a.mountGateways(ctx, mountOrder)
	if gatewayOperation != nil {
		operation = gatewayOperation
	}
	return operationID(operation), err
}

func (a *app) startOperation(ctx context.Context, changes *gordis.ChangeSet) (*gordis.Operation, error) {
	operation, err := changes.Apply(ctx)
	if operation != nil {
		a.mu.Lock()
		a.lastOperation = operation
		a.mu.Unlock()
	}
	return operation, err
}

func operationID(operation *gordis.Operation) uint64 {
	if operation == nil {
		return 0
	}
	return operation.Snapshot().ID
}

func (a *app) mountGateways(ctx context.Context, managed []*managedInstance) (*gordis.Operation, error) {
	if len(managed) == 0 {
		return nil, nil
	}
	changes := a.host.Changes()
	for _, instance := range managed {
		changes.Mount(instance.backend.Env(), gordis.InstanceSpec{
			ID: "gateway", Plugin: appplugin.GatewayPluginID, Config: instance.gatewayConfig,
		})
	}
	operation, err := a.startOperation(ctx, changes)
	if err == nil {
		err = operation.WaitReady(ctx)
	}
	return operation, err
}

func (a *app) setEnabled(ctx context.Context, id string, enabled bool) (uint64, error) {
	a.mu.Lock()
	managed := a.managed[id]
	a.mu.Unlock()
	if managed == nil {
		return 0, fmt.Errorf("unknown instance")
	}
	if enabled == (managed.backend != nil) {
		return 0, nil
	}
	if !enabled {
		changes := a.host.Changes()
		changes.Unmount(managed.backend)
		operation, err := a.startOperation(ctx, changes)
		if err == nil {
			err = operation.Wait(ctx)
		}
		if err == nil {
			a.mu.Lock()
			managed.backend = nil
			a.mu.Unlock()
		}
		return operationID(operation), err
	}

	changes := a.host.Changes()
	changes.Mount(a.host.Env().Isolate(appplugin.EndpointsKey, id), gordis.InstanceSpec{
		ID: id, Plugin: appplugin.BackendPluginID, Config: managed.backendConfig,
	})
	operation, err := a.startOperation(ctx, changes)
	if err != nil {
		return operationID(operation), err
	}
	mounted := operation.Mounted()
	if len(mounted) != 1 {
		return operationID(operation), fmt.Errorf("mounted backend handle is unavailable")
	}
	a.mu.Lock()
	managed.backend = mounted[0]
	a.mu.Unlock()
	if err = operation.WaitReady(ctx); err == nil {
		var gatewayOperation *gordis.Operation
		gatewayOperation, err = a.mountGateways(ctx, []*managedInstance{managed})
		if gatewayOperation != nil {
			operation = gatewayOperation
		}
	}
	return operationID(operation), err
}

func (a *app) uninstall(ctx context.Context, packageID string) (uint64, error) {
	a.mu.Lock()
	managed := make([]*managedInstance, 0)
	for _, instance := range a.managed {
		if instance.packageID == packageID {
			managed = append(managed, instance)
		}
	}
	a.mu.Unlock()
	if len(managed) == 0 {
		return 0, fmt.Errorf("package is not loaded")
	}
	changes := a.host.Changes()
	mounted := 0
	for _, instance := range managed {
		if instance.backend != nil {
			changes.Unmount(instance.backend)
			mounted++
		}
	}
	var operation *gordis.Operation
	var err error
	if mounted != 0 {
		operation, err = a.startOperation(ctx, changes)
		if err == nil {
			err = operation.Wait(ctx)
		}
		if err != nil {
			return operationID(operation), err
		}
	}
	a.mu.Lock()
	for _, instance := range managed {
		delete(a.managed, instance.id)
	}
	a.mu.Unlock()
	return operationID(operation), nil
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
