package opampsrv

import (
	"context"
	"io"
	"log"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// fakeConnection is a types.Connection that does nothing. It must be a
// comparable type because Agents stores connections as map keys.
type fakeConnection struct {
	id int
}

func (fakeConnection) Connection() net.Conn { return nil }

func (fakeConnection) Send(context.Context, *protobufs.ServerToAgent) error { return nil }

func (fakeConnection) Disconnect() error { return nil }

func newTestServer() *Server {
	return &Server{
		agents: &data.AllAgents,
		logger: &Logger{log.New(io.Discard, "", 0)},
	}
}

// The Server must report its capabilities in the first ServerToAgent message
// it sends on a connection, otherwise the Agent cannot know which features it
// may use.
func TestOnMessageReportsServerCapabilities(t *testing.T) {
	srv := newTestServer()
	conn := fakeConnection{id: 1}
	defer srv.agents.RemoveConnection(conn)

	response := srv.onMessage(context.Background(), conn, &protobufs.AgentToServer{
		InstanceUid: []byte("0123456789abcdef"),
		SequenceNum: 1,
	})

	require.NotNil(t, response)
	assert.Equal(t,
		uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus|
			protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig|
			protobufs.ServerCapabilities_ServerCapabilities_AcceptsEffectiveConfig|
			protobufs.ServerCapabilities_ServerCapabilities_OffersConnectionSettings|
			protobufs.ServerCapabilities_ServerCapabilities_AcceptsConnectionSettingsRequest),
		response.Capabilities)
}
