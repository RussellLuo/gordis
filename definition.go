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
	ErrBusy            = errors.New("gordis: another lifecycle operation is in progress")
	ErrClosed          = lifecycle.ErrClosed
	ErrStale           = errors.New("gordis: stale reference")
	ErrNotReady        = lifecycle.ErrNotReady
	ErrActivationCycle = errors.New("gordis: activation wait cycle")
)

// Plugin is both a registration prototype and a fresh object for one
// activation. This shared metadata interface exposes Spec; NewHost and the
// activation path additionally require both prototypes and objects returned by
// PluginSpec.New to implement Activate(context.Context, *Scope).
type Plugin = internalplugin.Plugin

// PluginSpec is stable plugin-type metadata. New must return a fresh, non-nil
// Plugin without acquiring resources or starting work.
type PluginSpec = internalplugin.Spec

// Validator optionally validates a plugin after its configuration is decoded.
// Validation must be repeatable and must not acquire lifecycle resources.
type Validator = internalplugin.Validator

// InstanceSpec contains desired-state configuration only. Plugin selects a
// registered PluginSpec by ID; it never stores a running Plugin object. Config
// must contain one JSON object; an empty value means {}. Standard struct decoding
// rejects unknown fields. Config is copied for every validation and activation.
type InstanceSpec struct {
	ID     string
	Plugin string
	Config json.RawMessage

	// view is supplied by Env. It is deliberately not constructible through an
	// InstanceSpec, keeping placement and visibility relative to the caller.
	view            View
	localID         string
	ownerGeneration uint64
	parent          string
	requiredLabels  map[string]string
	providedLabels  map[string]string
}

// Phase is the lifecycle position of the current generation. Failure is not a
// phase: a failed generation still proceeds through stopping and stopped while
// its original error is retained.
type Phase string

const (
	PhasePending    Phase = "pending"
	PhaseActivating Phase = "activating"
	PhaseReady      Phase = "ready"
	PhaseStopping   Phase = "stopping"
	PhaseStopped    Phase = "stopped"
)

// Outcome is final only after a generation reaches PhaseStopped.
type Outcome string

const (
	OutcomeSucceeded  Outcome = "succeeded"
	OutcomeFailed     Outcome = "failed"
	OutcomeIncomplete Outcome = "incomplete"
)

type TaskSnapshot = lifecycle.TaskSnapshot

// BindingSnapshot identifies the provider chosen for a consumer generation.
// Generation is zero before activation. After cleanup this is historical
// metadata only; it does not retain the service object.
type BindingSnapshot struct {
	Service    string `json:"service"`
	Label      string `json:"label"`
	Provider   string `json:"provider"`
	Generation uint64 `json:"generation"`
}

// Snapshot does not contain configuration or service values. CleanupComplete
// means all cleanup steps returned, not that failing callbacks freed resources.
type Snapshot struct {
	LocalID          string            `json:"localId,omitempty"`
	QualifiedID      string            `json:"qualifiedId"`
	Plugin           string            `json:"plugin"`
	Revision         uint64            `json:"revision"`
	BlockedLabels    []string          `json:"blockedLabels"`
	Generation       uint64            `json:"generation"`
	Phase            Phase             `json:"phase"`
	Parent           string            `json:"parent,omitempty"`
	ParentGeneration uint64            `json:"parentGeneration,omitempty"`
	Bindings         []BindingSnapshot `json:"bindings"`
	ProvidedLabels   map[string]string `json:"providedLabels"`
	Dependencies     []string          `json:"dependencies"`
	BlockedBy        []string          `json:"blockedBy"`
	StopReason       string            `json:"stopReason,omitempty"`
	Failure          string            `json:"failure,omitempty"`
	FailurePhase     string            `json:"failurePhase,omitempty"`
	Outcome          Outcome           `json:"outcome,omitempty"`
	CleanupPhase     string            `json:"cleanupPhase"`
	CleanupComplete  bool              `json:"cleanupComplete"`
	CleanupError     string            `json:"cleanupError,omitempty"`
	Residuals        []string          `json:"residuals"`
	Tasks            []TaskSnapshot    `json:"tasks"`
	Leases           int               `json:"leases"`
	View             map[string]string `json:"view,omitempty"`
}
