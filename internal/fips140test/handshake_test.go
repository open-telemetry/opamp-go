//go:debug fips140=only

package fips140test

import (
	"context"
	"crypto/fips140"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	servertypes "github.com/open-telemetry/opamp-go/server/types"
)

// TestWebSocketHandshakeUnderFIPS140Only connects a WebSocket client to a
// server while strict FIPS 140-3 enforcement is on. RFC 6455 computes
// Sec-WebSocket-Accept with SHA-1, which panics in this mode unless both the
// server upgrade and the client dial run outside enforcement.
func TestWebSocketHandshakeUnderFIPS140Only(t *testing.T) {
	require.True(t, fips140.Enabled(), "the //go:debug directive must put the test binary in FIPS 140-3 mode")

	gotMessage := make(chan *protobufs.AgentToServer, 1)
	callbacks := servertypes.Callbacks{
		OnConnecting: func(_ *http.Request) servertypes.ConnectionResponse {
			return servertypes.ConnectionResponse{
				Accept: true,
				ConnectionCallbacks: servertypes.ConnectionCallbacks{
					OnMessage: func(_ context.Context, _ servertypes.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
						select {
						case gotMessage <- message:
						default:
						}
						return &protobufs.ServerToAgent{InstanceUid: message.InstanceUid}
					},
				},
			}
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := server.New(nil)
	require.NoError(t, srv.Start(server.StartSettings{
		Settings:   server.Settings{Callbacks: callbacks},
		Listener:   ln,
		ListenPath: "/v1/opamp",
	}))
	t.Cleanup(func() { srv.Stop(context.Background()) })

	c := client.NewWebSocket(nil)
	require.NoError(t, c.SetAgentDescription(&protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{{
			Key:   "service.name",
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "fips140test"}},
		}},
	}))
	settings := types.StartSettings{
		OpAMPServerURL: "ws://" + ln.Addr().String() + "/v1/opamp",
		InstanceUid:    types.InstanceUid{0x01, 0x92, 0x3b, 0x3c, 0x4d, 0x5e, 0x7f, 0x80, 0x91, 0xa2, 0xb3, 0xc4, 0xd5, 0xe6, 0xf7, 0x08},
		Capabilities:   protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus,
	}
	require.NoError(t, c.Start(context.Background(), settings))
	t.Cleanup(func() { c.Stop(context.Background()) })

	select {
	case msg := <-gotMessage:
		require.NotNil(t, msg)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the first AgentToServer message, the WebSocket handshake did not complete under GODEBUG=fips140=only")
	}
}
