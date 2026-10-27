// Package app owns registration descriptions, configuration and runtime lifecycle.
package app

import (
	"fmt"
	"strings"

	"github.com/RussellLuo/gordis"
)

// Registration keeps application defaults alongside a plain Gordis prototype.
type Registration struct {
	Prototype   gordis.Plugin
	DefaultName string
}

// Application holds the registered types and their per-plugin runtime defaults.
type Application struct {
	host     *gordis.Host
	defaults map[string]string
}

// Compose validates descriptions and registers types without mounting instances.
func Compose(registrations ...Registration) (*Application, error) {
	prototypes := make([]gordis.Plugin, 0, len(registrations))
	for i, registration := range registrations {
		if registration.Prototype == nil || strings.TrimSpace(registration.DefaultName) == "" {
			return nil, fmt.Errorf("registration %d needs a prototype and nonempty default name", i)
		}
		prototypes = append(prototypes, registration.Prototype)
	}

	host, err := gordis.NewHost(prototypes...)
	if err != nil {
		return nil, err
	}

	defaults := make(map[string]string, len(registrations))
	for _, registration := range registrations {
		defaults[registration.Prototype.Spec().ID] = registration.DefaultName
	}
	return &Application{host: host, defaults: defaults}, nil
}
