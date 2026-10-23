package gordisrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"

	plugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// ProgramDescription describes the child-relative contracts and program config.
// It exposes no local plugin or instance identities.
type ProgramDescription struct {
	Identity     Identity
	Contracts    []Contract
	ConfigSchema json.RawMessage
}

func newProgramClient(command Command, limits Limits) (*plugin.Client, *exec.Cmd) {
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Env = append([]string{}, command.Env...)
	client := plugin.NewClient(&plugin.ClientConfig{SkipHostEnv: true, HandshakeConfig: handshake, Plugins: map[string]plugin.Plugin{"unit": new(transport)}, Cmd: cmd, AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, StartTimeout: limits.StartupTimeout, GRPCDialOptions: []grpc.DialOption{grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(limits.MaxMessage), grpc.MaxCallSendMsgSize(limits.MaxMessage))}})
	return client, cmd
}

func describeRemote(ctx context.Context, remote *remote) (description, *configSchema, error) {
	var d description
	if err := invoke(ctx, remote.conn, "Describe", packet{}, &d); err != nil {
		return d, nil, err
	}
	if err := d.Identity.validate(); err != nil {
		return d, nil, err
	}
	if err := validateDeclarations(d.Bindings); err != nil {
		return d, nil, err
	}
	schema, err := compileConfigSchema(d.ConfigSchema)
	return d, schema, err
}

func validateDeclarations(bindings []declaration) error {
	seen := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		key := b.Direction + ":" + b.Name
		if b.Name == "" || b.Version == "" || (b.Direction != "import" && b.Direction != "export") || seen[key] {
			return errors.New("invalid program contracts")
		}
		seen[key] = true
	}
	return nil
}

// DescribeProgram executes a program and reaps it before returning. It performs
// only Describe: no Scope, callbacks, initialization or business cleanup.
func DescribeProgram(ctx context.Context, command Command, limits Limits) (ProgramDescription, error) {
	if command.Path == "" {
		return ProgramDescription{}, errors.New("empty command")
	}
	limits = limits.defaults()
	ctx, cancel := context.WithTimeout(ctx, limits.StartupTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ProgramDescription{}, err
	}
	client, _ := newProgramClient(command, limits)
	defer client.Kill()
	stopCancel := context.AfterFunc(ctx, client.Kill)
	defer stopCancel()
	connection, err := client.Client()
	if err != nil {
		if ctx.Err() != nil {
			return ProgramDescription{}, ctx.Err()
		}
		return ProgramDescription{}, err
	}
	raw, err := connection.Dispense("unit")
	if err != nil {
		return ProgramDescription{}, err
	}
	d, _, err := describeRemote(ctx, raw.(*remote))
	if err != nil {
		return ProgramDescription{}, err
	}
	out := ProgramDescription{Identity: d.Identity, ConfigSchema: bytes.Clone(d.ConfigSchema), Contracts: make([]Contract, 0, len(d.Bindings))}
	for _, b := range d.Bindings {
		out.Contracts = append(out.Contracts, Contract{ID: b.Name, Version: b.Version, Direction: b.Direction})
	}
	return out, nil
}
