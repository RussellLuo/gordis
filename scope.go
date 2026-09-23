package gordis

import "github.com/RussellLuo/gordis/internal/lifecycle"

// Scope owns one generation's tasks, request leases, and cleanup callbacks.
// Do not retain it across activations. All registration methods are concurrency safe.
// Lifecycle control stays in the framework; plugins only receive this facade.
type Scope = lifecycle.Scope

func invoke(fn func() error) error { return lifecycle.Invoke(fn) }
