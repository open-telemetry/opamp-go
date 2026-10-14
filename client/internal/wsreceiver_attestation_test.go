package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

// runAttestedReceiver connects to a server that runs serve on the upgraded
// connection, and runs an attested receiver until its loop exits.
func runAttestedReceiver(t *testing.T, v signing.Verifier, serve func(conn *websocket.Conn)) (*wsReceiver, *websocket.Conn) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serve(conn)
	}))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	callbacks := types.Callbacks{}
	callbacks.SetDefaults()
	state := &ClientSyncedState{}
	caps := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus
	require.NoError(t, state.SetCapabilities(&caps))
	r := NewWSReceiver(&sharedinternal.NopLogger{}, callbacks, conn, NewSender(&sharedinternal.NopLogger{}), state, nil,
		new(sync.Mutex), time.Second, v, "ws://"+testHost+"/v1/opamp", nil)

	r.ReceiverLoop(context.Background())
	select {
	case <-r.IsStopped():
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not stop")
	}
	return r, conn
}

func writeProto(t *testing.T, conn *websocket.Conn, m proto.Message) {
	t.Helper()
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, mustMarshal(t, m)))
}

func TestWSReceiverAttestationFailureClosesConnection(t *testing.T) {
	pki := newTestPKI(t)
	r, conn := runAttestedReceiver(t, pki.verifier(t), func(c *websocket.Conn) {
		writeProto(t, c, plainMsg) // unsigned, not a heartbeat
	})
	assert.True(t, r.WasAttestationFailure())
	assert.False(t, r.WasConnectionError())
	assert.False(t, r.WasAttested())
	assert.Error(t, conn.WriteMessage(websocket.BinaryMessage, []byte{0}), "the connection was closed")
}

func TestWSReceiverAttestedThenNormalClose(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	r, _ := runAttestedReceiver(t, pki.verifier(t), func(c *websocket.Conn) {
		writeProto(t, c, signedMsg(t, s, plainMsg, true))
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	})
	assert.True(t, r.WasAttested())
	assert.False(t, r.WasAttestationFailure())
	assert.False(t, r.WasConnectionError())
}

func TestWSReceiverAbnormalCloseIsConnectionError(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	r, _ := runAttestedReceiver(t, pki.verifier(t), func(c *websocket.Conn) {
		writeProto(t, c, signedMsg(t, s, plainMsg, true))
		_ = c.Close() // no close handshake
	})
	assert.True(t, r.WasAttested())
	assert.True(t, r.WasConnectionError())
	assert.False(t, r.WasAttestationFailure())
}

func TestNewWSReceiverUnparsableURLFailsClosed(t *testing.T) {
	pki := newTestPKI(t)
	r := NewWSReceiver(&sharedinternal.NopLogger{}, types.Callbacks{}, nil, nil, &ClientSyncedState{}, nil,
		new(sync.Mutex), time.Second, pki.verifier(t), "://bad url", nil)
	require.NotNil(t, r.attestation)
	assert.Empty(t, r.attestation.serverName)

	_, err := r.attestation.ProcessEnvelope(context.Background(), signedMsg(t, pki.signer(t), plainMsg, true))
	require.ErrorIs(t, err, signing.ErrServerNameRequired)
	assert.False(t, (&wsReceiver{}).WasAttested(), "no attestation state means not attested")
}
