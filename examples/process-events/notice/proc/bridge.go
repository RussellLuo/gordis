// Package proc implements the optional process boundary for notice.
// The business Plugin remains a normal Local-first Gordis Plugin.
package proc

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/examples/process-events/notice"
	"github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/gordis/processbridge"
)

// Resolver supplies application-owned process settings such as the executable
// path. New adds the protocol identity and per-instance business configuration.
type Resolver func() (process.Options, error)

var identity = process.Identity{
	Protocol: process.Protocol,
	Package:  "example-process-events",
	Version:  "1.0.0",
}

// New returns the process-backed prototype registered with a Host.
func New(resolve Resolver) (gordis.Plugin, error) {
	if resolve == nil {
		return nil, errors.New("notice/proc: nil resolver")
	}

	return (processbridge.Adapter{
		ID:     notice.PluginID,
		Events: []processbridge.EventBinding{binding},
		Resolve: func(raw json.RawMessage) (process.Options, error) {
			resolved, err := resolve()
			if err != nil {
				return process.Options{}, err
			}
			resolved.Identity = identity
			resolved.Config = append(json.RawMessage(nil), raw...)
			return resolved, nil
		},
	}).Plugin()
}

// Serve runs the same business Plugin in a child process.
func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser) error {
	return processbridge.Serve(ctx, in, out, processbridge.ServeOptions{
		Identity: identity,
		Plugin:   new(notice.Plugin),
		Events:   []processbridge.EventBinding{binding},
	})
}

var binding = processbridge.BindTopic(
	notice.Topic,
	processbridge.JSONEventCodec[notice.Notice]("example.process-events.notice.json/1"),
	processbridge.EventPublish,
)
