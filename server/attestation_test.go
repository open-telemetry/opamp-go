package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
	"github.com/open-telemetry/opamp-go/signing"
)

const (
	requiresBit = uint64(protobufs.AgentCapabilities_AgentCapabilities_RequiresPayloadTrustVerification)
	tofuBit     = uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsPayloadTrustAnchorTOFU)
	offersBit   = uint64(protobufs.ServerCapabilities_ServerCapabilities_OffersPayloadTrustVerification)
)

var attestMsg = &protobufs.ServerToAgent{InstanceUid: testInstanceUid, Flags: 1}

func TestSignOutgoingChainDelivery(t *testing.T) {
	pki := newAttestPKI(t)
	signer := &rotatingSigner{cur: pki.signer(t), anchor: pki.caPEM}
	state := newConnectionSigningState(context.Background(), signer, true)
	ctx := context.Background()

	env, err := state.signOutgoing(ctx, attestMsg)
	require.NoError(t, err)
	assert.True(t, proto.Equal(attestMsg, pki.verifyEnvelope(t, env)))
	assert.Equal(t, pki.caPEM, env.TrustChainResponse.TofuTrustAnchor)

	env, err = state.signOutgoing(ctx, attestMsg)
	require.NoError(t, err)
	assert.Nil(t, env.TrustChainResponse, "an unchanged chain is not re-sent")
	assert.NotEmpty(t, env.Signature)

	signer.set(pki.signer(t))
	env, err = state.signOutgoing(ctx, attestMsg)
	require.NoError(t, err)
	pki.verifyEnvelope(t, env)
	assert.Empty(t, env.TrustChainResponse.TofuTrustAnchor, "the TOFU anchor is only sent first")
}

func TestSignOutgoingTOFUAnchorUnavailable(t *testing.T) {
	pki := newAttestPKI(t)
	local := pki.signer(t)
	tests := map[string]signing.Signer{
		"no TrustAnchorProvider": signerFunc(local.Sign),
		"TrustAnchorPEM error":   anchorErrSigner{local},
	}
	for name, signer := range tests {
		t.Run(name, func(t *testing.T) {
			state := newConnectionSigningState(context.Background(), signer, true)
			env, err := state.signOutgoing(context.Background(), attestMsg)
			require.NoError(t, err)
			require.NotNil(t, env.TrustChainResponse)
			assert.NotEmpty(t, env.TrustChainResponse.ErrorMessage)
			assert.Empty(t, env.TrustChainResponse.CertificateChain)

			env, err = state.signOutgoing(context.Background(), attestMsg)
			require.NoError(t, err)
			assert.Nil(t, env.TrustChainResponse, "the error is reported once")
		})
	}
}

func TestSignOutgoingSignerBehaviour(t *testing.T) {
	boom := errors.New("signer down")
	state := newConnectionSigningState(context.Background(), signerFunc(func(context.Context, []byte) (signing.SignResult, error) {
		return signing.SignResult{}, boom
	}), false)
	_, err := state.signOutgoing(context.Background(), attestMsg)
	require.ErrorIs(t, err, boom)

	// A re-marshalling signer's bytes, not the server's, are transmitted.
	pki := newAttestPKI(t)
	local := pki.signer(t)
	remarshalled := []byte{0x0a, 0x02, 'h', 'i'}
	state = newConnectionSigningState(context.Background(), signerFunc(func(ctx context.Context, _ []byte) (signing.SignResult, error) {
		return local.Sign(ctx, remarshalled)
	}), false)
	env, err := state.signOutgoing(context.Background(), attestMsg)
	require.NoError(t, err)
	assert.Equal(t, remarshalled, env.Payload)
}

func TestAttestationCapabilityHelpers(t *testing.T) {
	assert.True(t, agentRequiresAttestation(requiresBit|tofuBit))
	assert.False(t, agentRequiresAttestation(tofuBit))
	assert.True(t, agentRequestsTOFU(tofuBit))
	assert.False(t, agentRequestsTOFU(requiresBit))
	assert.Equal(t, offersBit|1, addOffersAttestationBit(1))
	assert.Equal(t, offersBit, addOffersAttestationBit(offersBit))
}

// wsPair returns a server-side and client-side websocket connection pair.
func wsPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	serverConns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			serverConns <- conn
		}
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	server := <-serverConns
	t.Cleanup(func() { client.Close(); server.Close() })
	return server, client
}

func readWS(t *testing.T, conn *websocket.Conn, m proto.Message) {
	t.Helper()
	_, b, err := conn.ReadMessage()
	require.NoError(t, err)
	require.NoError(t, sharedinternal.DecodeWSMessage(b, m))
}

func TestWSConnectionSendAttested(t *testing.T) {
	pki := newAttestPKI(t)
	serverConn, clientConn := wsPair(t)
	conn := newWSConnection(serverConn, sharedinternal.DefaultMaxMessageSize, true)
	ctx := context.Background()

	require.ErrorIs(t, conn.Send(ctx, attestMsg), ErrSendBeforeNegotiated)

	conn.enableSigning(newConnectionSigningState(ctx, pki.signer(t), false))
	conn.markNegotiated()
	assert.True(t, conn.isNegotiated())

	heartbeat := &protobufs.ServerToAgent{InstanceUid: testInstanceUid}
	require.NoError(t, conn.Send(ctx, heartbeat))
	var gotHeartbeat protobufs.ServerToAgent
	readWS(t, clientConn, &gotHeartbeat)
	assert.True(t, proto.Equal(heartbeat, &gotHeartbeat), "heartbeats are sent unsigned")

	require.NoError(t, conn.Send(ctx, attestMsg))
	var env protobufs.SignedServerToAgent
	readWS(t, clientConn, &env)
	assert.True(t, proto.Equal(attestMsg, pki.verifyEnvelope(t, &env)))
}

func TestWSConnectionSendWithoutSigner(t *testing.T) {
	serverConn, clientConn := wsPair(t)
	conn := newWSConnection(serverConn, sharedinternal.DefaultMaxMessageSize, false)
	require.NoError(t, conn.Send(context.Background(), attestMsg))
	var got protobufs.ServerToAgent
	readWS(t, clientConn, &got)
	assert.True(t, proto.Equal(attestMsg, &got))
}

func TestWSConnectionSendSignerError(t *testing.T) {
	serverConn, _ := wsPair(t)
	conn := newWSConnection(serverConn, sharedinternal.DefaultMaxMessageSize, true)
	boom := errors.New("signer down")
	conn.enableSigning(newConnectionSigningState(context.Background(), signerFunc(func(context.Context, []byte) (signing.SignResult, error) {
		return signing.SignResult{}, boom
	}), false))
	conn.markNegotiated()
	require.ErrorIs(t, conn.Send(context.Background(), attestMsg), boom)
}

// startAttestedServer starts a server with signer whose OnMessage returns respond(msg).
func startAttestedServer(t *testing.T, signer signing.Signer, respond func(*protobufs.AgentToServer) *protobufs.ServerToAgent) *StartSettings {
	t.Helper()
	settings := &StartSettings{Settings: Settings{
		PayloadSigner:      signer,
		CustomCapabilities: []string{"local.test.capability"},
		Callbacks: types.Callbacks{
			OnConnecting: func(*http.Request) types.ConnectionResponse {
				return types.ConnectionResponse{Accept: true, ConnectionCallbacks: types.ConnectionCallbacks{
					OnMessage: func(_ context.Context, _ types.Connection, m *protobufs.AgentToServer) *protobufs.ServerToAgent {
						return respond(m)
					},
				}}
			},
		},
	}}
	srv := startServer(t, settings)
	t.Cleanup(func() { srv.Stop(context.Background()) })
	return settings
}

func echoUID(m *protobufs.AgentToServer) *protobufs.ServerToAgent {
	return &protobufs.ServerToAgent{InstanceUid: m.InstanceUid, Flags: 1}
}

func postHTTP(t *testing.T, settings *StartSettings, caps uint64) (*http.Response, []byte) {
	t.Helper()
	b, err := proto.Marshal(&protobufs.AgentToServer{InstanceUid: testInstanceUid, Capabilities: caps})
	require.NoError(t, err)
	resp, err := http.Post("http://"+settings.ListenEndpoint+settings.ListenPath, contentTypeProtobuf, bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, body
}

func TestServerPlainHTTPAttestation(t *testing.T) {
	pki := newAttestPKI(t)

	t.Run("signed response", func(t *testing.T) {
		settings := startAttestedServer(t, pki.signer(t), echoUID)
		resp, body := postHTTP(t, settings, requiresBit)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var env protobufs.SignedServerToAgent
		require.NoError(t, proto.Unmarshal(body, &env))
		inner := pki.verifyEnvelope(t, &env)
		assert.Equal(t, offersBit, inner.Capabilities&offersBit)
		assert.Equal(t, settings.CustomCapabilities, inner.CustomCapabilities.Capabilities)
	})

	t.Run("no-op response is an unsigned heartbeat", func(t *testing.T) {
		settings := startAttestedServer(t, pki.signer(t), func(*protobufs.AgentToServer) *protobufs.ServerToAgent { return nil })
		_, body := postHTTP(t, settings, requiresBit)
		var msg protobufs.ServerToAgent
		require.NoError(t, proto.Unmarshal(body, &msg))
		assert.True(t, protobufs.IsHeartbeatServerToAgent(&msg))
		assert.Equal(t, testInstanceUid, msg.InstanceUid)
	})

	t.Run("signer error", func(t *testing.T) {
		failing := signerFunc(func(context.Context, []byte) (signing.SignResult, error) {
			return signing.SignResult{}, errors.New("signer down")
		})
		settings := startAttestedServer(t, failing, echoUID)
		resp, _ := postHTTP(t, settings, requiresBit)
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})

	t.Run("agent without Requires bit", func(t *testing.T) {
		settings := startAttestedServer(t, pki.signer(t), echoUID)
		_, body := postHTTP(t, settings, 0)
		var msg protobufs.ServerToAgent
		require.NoError(t, proto.Unmarshal(body, &msg))
		assert.Equal(t, testInstanceUid, msg.InstanceUid)
		assert.Equal(t, offersBit, msg.Capabilities&offersBit)
		assert.Equal(t, settings.CustomCapabilities, msg.CustomCapabilities.Capabilities)
	})
}

func dialAndSend(t *testing.T, settings *StartSettings, caps uint64) *websocket.Conn {
	t.Helper()
	conn, _, err := dialClient(settings)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	b, err := proto.Marshal(&protobufs.AgentToServer{InstanceUid: testInstanceUid, Capabilities: caps})
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, b))
	return conn
}

func TestServerWebSocketAttestation(t *testing.T) {
	pki := newAttestPKI(t)

	t.Run("signed response", func(t *testing.T) {
		settings := startAttestedServer(t, pki.signer(t), echoUID)
		var env protobufs.SignedServerToAgent
		readWS(t, dialAndSend(t, settings, requiresBit), &env)
		inner := pki.verifyEnvelope(t, &env)
		assert.Equal(t, offersBit, inner.Capabilities&offersBit)
		assert.Empty(t, env.TrustChainResponse.TofuTrustAnchor)
	})

	t.Run("TOFU response carries the anchor", func(t *testing.T) {
		settings := startAttestedServer(t, pki.signer(t).WithRootCA(pki.ca), echoUID)
		var env protobufs.SignedServerToAgent
		readWS(t, dialAndSend(t, settings, requiresBit|tofuBit), &env)
		pki.verifyEnvelope(t, &env)
		assert.Equal(t, pki.caPEM, env.TrustChainResponse.TofuTrustAnchor)
	})

	t.Run("agent without Requires bit", func(t *testing.T) {
		settings := startAttestedServer(t, pki.signer(t), echoUID)
		var msg protobufs.ServerToAgent
		readWS(t, dialAndSend(t, settings, 0), &msg)
		assert.Equal(t, testInstanceUid, msg.InstanceUid)
		assert.Equal(t, offersBit, msg.Capabilities&offersBit)
	})
}

func TestSignOutgoingMarshalError(t *testing.T) {
	state := newConnectionSigningState(context.Background(), newAttestPKI(t).signer(t), false)
	_, err := state.signOutgoing(context.Background(), &protobufs.ServerToAgent{
		ErrorResponse: &protobufs.ServerErrorResponse{ErrorMessage: "\xff"},
	})
	require.Error(t, err)
}
