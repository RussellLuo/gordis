package gordisrpc

import (
	"context"
	plugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

var handshake = plugin.HandshakeConfig{ProtocolVersion: 1, MagicCookieKey: "GORDIS_RPC", MagicCookieValue: Protocol}

type transport struct {
	plugin.NetRPCUnsupportedPlugin
	server *unitServer
}
type remote struct {
	conn   *grpc.ClientConn
	broker *plugin.GRPCBroker
}

func (t *transport) GRPCServer(b *plugin.GRPCBroker, s *grpc.Server) error {
	t.server.broker = b
	registerRPC(s, t.server)
	return nil
}
func (*transport) GRPCClient(_ context.Context, b *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return &remote{c, b}, nil
}
