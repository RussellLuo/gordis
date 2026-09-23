// Package gordis manages plugin instances, typed services, and resources.
// Native plugin constructors are statically linked; instance graphs can change
// at runtime.
// Application-specific registries and browser SDKs belong to the application.
package gordis

import (
	"encoding/json"
	"errors"

	"github.com/RussellLuo/gordis/internal/lifecycle"
	internalplugin "github.com/RussellLuo/gordis/internal/plugin"
)

var (
	ErrBusy   = errors.New("gordis: another lifecycle operation is in progress")
	ErrClosed = lifecycle.ErrClosed
	ErrStale  = errors.New("gordis: stale change plan")
)

// Plugin is both a registration prototype and a fresh object for one
// activation. A Host calls Spec on prototypes and calls Start only on objects
// returned by PluginSpec.New. Start returns only when ready; ctx bounds startup
// and Scope.Context bounds long-lived work.
type Plugin = internalplugin.Plugin

// PluginSpec is stable plugin-type metadata. New must return a fresh, non-nil
// Plugin without acquiring resources or starting work.
type PluginSpec = internalplugin.Spec

// Validator optionally validates a plugin after its configuration is decoded.
// Validation must be repeatable and must not acquire lifecycle resources.
type Validator = internalplugin.Validator

// InstanceSpec contains desired-state configuration only. Plugin selects a
// registered PluginSpec by ID; it never stores a running Plugin object. Config
// and binding maps are copied by NewHost and for every validation/activation.
type InstanceSpec struct {
	ID     string
	Plugin string
	Config json.RawMessage
	// Disabled is desired state, independent of actual cleanup/readiness.
	Disabled bool
	// AllowPending explicitly permits waiting for services during dynamic changes.
	// NewHost still requires a complete initial graph.
	AllowPending bool
	// Version is a public, non-secret package/configuration label for diagnostics.
	Version string
	// Parent owns this instance, independently of service dependencies.
	Parent string
	// Optional excludes this child's subtree from its parent's group readiness.
	// A startup failure cleans up that branch and its service consumers while
	// preserving the healthy parent. Start still returns the error. Optional does
	// not relax Requires validation or protect consumers from dependency failure.
	Optional bool
	// Inputs and Outputs map declared service names to deployment slots.
	// Use Key.Name to obtain map keys. Undeclared names and empty slots are
	// errors; omitted keys use their name.
	Inputs  map[string]string
	Outputs map[string]string
}

type State string

const (
	Registered State = "registered"
	Pending    State = "pending"
	Starting   State = "starting"
	Ready      State = "ready"
	Stopping   State = "stopping"
	Stopped    State = "stopped"
	Failed     State = "failed"
)

type TaskSnapshot = lifecycle.TaskSnapshot

// BindingSnapshot identifies the provider chosen for a consumer generation.
// Generation is zero before activation. After cleanup this is historical
// metadata only; it does not retain the service object.
type BindingSnapshot struct {
	Service    string `json:"service"`
	Slot       string `json:"slot"`
	Provider   string `json:"provider"`
	Generation uint64 `json:"generation"`
}

// Snapshot does not contain configuration or service values. CleanupComplete
// means all cleanup steps returned, not that failing callbacks freed resources.
type Snapshot struct {
	ID               string            `json:"id"`
	Plugin           string            `json:"plugin"`
	DesiredEnabled   bool              `json:"desiredEnabled"`
	Revision         uint64            `json:"revision"`
	Version          string            `json:"version,omitempty"`
	BlockedSlots     []string          `json:"blockedSlots"`
	Generation       uint64            `json:"generation"`
	State            State             `json:"state"`
	Parent           string            `json:"parent,omitempty"`
	ParentGeneration uint64            `json:"parentGeneration,omitempty"`
	Optional         bool              `json:"optional,omitempty"`
	GroupReady       bool              `json:"groupReady"`
	Bindings         []BindingSnapshot `json:"bindings"`
	ProvidedSlots    map[string]string `json:"providedSlots"`
	Dependencies     []string          `json:"dependencies"`
	BlockedBy        []string          `json:"blockedBy"`
	StopReason       string            `json:"stopReason,omitempty"`
	LastError        string            `json:"lastError,omitempty"`
	FailurePhase     string            `json:"failurePhase,omitempty"`
	CleanupPhase     string            `json:"cleanupPhase"`
	CleanupComplete  bool              `json:"cleanupComplete"`
	Residuals        []string          `json:"residuals"`
	Tasks            []TaskSnapshot    `json:"tasks"`
	Leases           int               `json:"leases"`
}
