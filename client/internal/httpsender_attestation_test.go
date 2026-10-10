package internal

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var plainMsg = &protobufs.ServerToAgent{
	InstanceUid:  []byte("0123456789abcdef"),
	Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
}

func newAttestedHTTPSender(t *testing.T, pki *testPKI) *HTTPSender {
	t.Helper()
	sender := newTestHTTPSender()
	callbacks := types.Callbacks{}
	callbacks.SetDefaults()
	state := &ClientSyncedState{}
	sender.receiveProcessor = newReceivedProcessor(&sharedinternal.NopLogger{}, callbacks, sender, state, nil, new(sync.Mutex), time.Second)
	sender.attestation = newAttestationState(pki.verifier(t), testHost, nil)
	return sender
}

func httpResponse(body []byte) *http.Response {
	return &http.Response{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}
}

func TestHTTPSenderReceiveResponseAttestation(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	ctx := context.Background()
	sender := newAttestedHTTPSender(t, pki)

	failed := sender.receiveResponse(ctx, httpResponse(mustMarshal(t, signedMsg(t, s, plainMsg, true))))
	assert.False(t, failed)
	assert.True(t, sender.attestation.firstSeen)

	bad := signedMsg(t, s, plainMsg, false)
	bad.Signature[len(bad.Signature)-1] ^= 0xff
	assert.True(t, sender.receiveResponse(ctx, httpResponse(mustMarshal(t, bad))))
	assert.False(t, sender.attestation.firstSeen, "an attestation failure resets the handshake")

	// After the reset a chainless response is rejected until the chain is re-sent.
	assert.True(t, sender.receiveResponse(ctx, httpResponse(mustMarshal(t, signedMsg(t, s, plainMsg, false)))))
	assert.False(t, sender.receiveResponse(ctx, httpResponse(mustMarshal(t, signedMsg(t, s, plainMsg, true)))))
}

func TestHTTPSenderReceiveResponseNonAttestationErrors(t *testing.T) {
	pki := newTestPKI(t)
	sender := newAttestedHTTPSender(t, pki)
	resp := httpResponse([]byte("not gzip"))
	resp.Header.Set(headerContentEncoding, encodingTypeGZip)
	assert.False(t, sender.receiveResponse(context.Background(), resp), "a body read error is not an attestation failure")

	// A validly signed but undecodable payload is logged, not treated as an attestation failure.
	env := signedEnvelope(t, pki.signer(t), []byte{0xff}, true)
	assert.False(t, sender.receiveResponse(context.Background(), httpResponse(mustMarshal(t, env))))
}

// runAttestedSender runs Run against a mock server whose responses come from respond.
func runAttestedSender(t *testing.T, respond func(n int32) []byte) (*HTTPSender, *atomic.Int32, context.CancelFunc, chan struct{}) {
	t.Helper()
	pki := newTestPKI(t)
	var requests atomic.Int32
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)
	srv.SetOnRequest(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write(respond(requests.Add(1)))
	})

	sender := newTestHTTPSender()
	queueMessage(sender)
	callbacks := types.Callbacks{}
	callbacks.SetDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.Run(ctx, "http://"+srv.Endpoint, callbacks, &ClientSyncedState{}, nil, new(sync.Mutex), time.Second, pki.verifier(t), nil)
	}()
	return sender, &requests, cancel, done
}

// queueMessage makes a non-empty AgentToServer pending and schedules a send.
func queueMessage(sender *HTTPSender) {
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.InstanceUid = plainMsg.InstanceUid
		msg.SequenceNum++
	})
	sender.ScheduleSend()
}

func TestHTTPSenderRunBacksOffAfterAttestationFailure(t *testing.T) {
	heartbeat := mustMarshal(t, &protobufs.ServerToAgent{InstanceUid: plainMsg.InstanceUid})
	unsigned := mustMarshal(t, plainMsg)
	sender, requests, cancel, done := runAttestedSender(t, func(n int32) []byte {
		if n == 1 {
			return unsigned
		}
		return heartbeat
	})

	// The rejected response triggers a backoff, after which the next send goes out.
	require.Eventually(t, func() bool { return requests.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	queueMessage(sender)
	require.Eventually(t, func() bool { return requests.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.NotNil(t, sender.attestation)
	assert.Equal(t, "127.0.0.1", sender.attestation.serverName)
}

func TestHTTPSenderRunStopsDuringAttestationBackoff(t *testing.T) {
	unsigned := mustMarshal(t, plainMsg)
	_, requests, cancel, done := runAttestedSender(t, func(int32) []byte { return unsigned })

	require.Eventually(t, func() bool { return requests.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop while backing off")
	}
}

func TestHTTPSenderMakeOneRequestRoundtripWithoutResponse(t *testing.T) {
	sender := newAttestedHTTPSender(t, newTestPKI(t))
	assert.False(t, sender.makeOneRequestRoundtrip(context.Background()), "nothing pending")

	sender.url = "://bad url"
	queueMessage(sender)
	assert.False(t, sender.makeOneRequestRoundtrip(context.Background()), "request cannot be built")
}
