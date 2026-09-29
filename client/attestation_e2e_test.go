package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	servertypes "github.com/open-telemetry/opamp-go/server/types"
	"github.com/open-telemetry/opamp-go/signing"
)

// requireNeverReceived asserts the client does not accept name within d.
func requireNeverReceived(t *testing.T, c *e2eClient, name string, d time.Duration) {
	t.Helper()
	require.Never(t, func() bool { return c.received(name) }, d, 20*time.Millisecond,
		"client accepted RemoteConfig %q", name)
}

// requireBackoff asserts the server sees repeated attempts that are neither
// absent nor a tight loop over window.
func requireBackoff(t *testing.T, count func() int32, window time.Duration, maxAttempts int32) {
	t.Helper()
	start := count()
	time.Sleep(window)
	got := count() - start
	assert.GreaterOrEqual(t, got, int32(1), "client stopped retrying")
	assert.Less(t, got, maxAttempts, "client retried in a tight loop")
}

func TestAttestationE2EFixedAnchor(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			pki := newE2EPKI(t)
			srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{signer: pki.signer})
			c := startE2EClient(t, tr, srv.addr, pki.fixedAnchor(t), attestedCaps)

			c.requireReceived(t, "server")
			assert.True(t, srv.sawRequires.Load())
			assert.Zero(t, c.logger.attestationFailures.Load())
		})
	}
}

func TestAttestationE2ETOFU(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			pki := newE2EPKI(t)
			srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{signer: pki.signer})
			path := filepath.Join(t.TempDir(), "anchor.pem")

			c := startE2EClient(t, tr, srv.addr, signing.TOFUAnchor(signing.NewFileTOFUStore(path)), tofuCaps)
			c.requireReceived(t, "server")
			stored, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, pki.caPEM, stored)
			c.stop()

			// A restarted client loads the stored anchor instead of enrolling.
			store := &countingStore{TOFUStore: signing.NewFileTOFUStore(path)}
			c2 := startE2EClient(t, tr, srv.addr, signing.TOFUAnchor(store), tofuCaps)
			c2.requireReceived(t, "server")
			assert.Zero(t, store.saves.Load())
		})
	}
}

// After enrollment, reconnecting to the same server reuses the enrolled
// verifier rather than enrolling again.
func TestAttestationE2ETOFUReconnectDoesNotReenroll(t *testing.T) {
	pki := newE2EPKI(t)
	srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{
		signer: pki.signer,
		onMessage: func(n int32, _ *protobufs.AgentToServer) *protobufs.ServerToAgent {
			return &protobufs.ServerToAgent{RemoteConfig: remoteConfig(fmt.Sprintf("msg%d", n))}
		},
	})
	store := &countingStore{TOFUStore: signing.NewFileTOFUStore(filepath.Join(t.TempDir(), "anchor.pem"))}
	c := startE2EClient(t, transports[0], srv.addr, signing.TOFUAnchor(store), tofuCaps)
	c.requireReceived(t, "msg1")

	srv.disconnect()
	require.Eventually(t, func() bool { return srv.connects.Load() >= 2 }, e2eTimeout, 10*time.Millisecond)
	// The first message on the new connection carries the chain again.
	require.Eventually(t, func() bool { return c.configCount() >= 2 }, e2eTimeout, 10*time.Millisecond)
	assert.Equal(t, int32(1), store.saves.Load(), "reconnect must not re-enroll")
}

// A reconnect to a server under a different CA must not be trusted, even
// though the client still advertises AcceptsPayloadTrustAnchorTOFU.
func TestAttestationE2ETOFUPinnedAcrossReconnect(t *testing.T) {
	addr := testhelpers.GetAvailableLocalAddress()
	first := newE2EPKI(t)
	srvA := startE2EServer(t, addr, e2eServerOpts{signer: first.signer, name: "first"})
	path := filepath.Join(t.TempDir(), "anchor.pem")
	c := startE2EClient(t, transports[0], addr, signing.TOFUAnchor(signing.NewFileTOFUStore(path)), tofuCaps)
	c.requireReceived(t, "first")

	srvA.stop()
	impostor := newE2EPKI(t)
	srvB := startE2EServer(t, addr, e2eServerOpts{signer: impostor.signer, name: "impostor"})

	require.Eventually(t, func() bool { return srvB.messages.Load() >= 1 }, e2eTimeout, 10*time.Millisecond)
	requireNeverReceived(t, c, "impostor", time.Second)
	assert.Positive(t, c.logger.attestationFailures.Load())

	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first.caPEM, stored, "the pinned anchor must not be replaced")
}

func TestAttestationE2EWrongAnchor(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			server := newE2EPKI(t)
			other := newE2EPKI(t)
			srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{signer: server.signer})
			c := startE2EClient(t, tr, srv.addr, other.fixedAnchor(t), attestedCaps)

			require.Eventually(t, func() bool { return c.logger.attestationFailures.Load() >= 1 }, e2eTimeout, 10*time.Millisecond)
			requireBackoff(t, srv.messages.Load, 2*time.Second, 20)
			assert.False(t, c.received("server"))
		})
	}
}

func TestAttestationE2EHostnameMismatch(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			pki := newE2EPKI(t, "other.example")
			srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{signer: pki.signer})
			c := startE2EClient(t, tr, srv.addr, pki.fixedAnchor(t), attestedCaps)

			require.Eventually(t, func() bool { return c.logger.attestationFailures.Load() >= 1 }, e2eTimeout, 10*time.Millisecond)
			assert.False(t, c.received("server"))
		})
	}
}

// A leaf rotation mid-connection re-delivers the chain without reconnecting.
func TestAttestationE2ERotationWithoutReconnect(t *testing.T) {
	pki := newE2EPKI(t)
	signer := &rotatingSigner{a: pki.signer, b: pki.newSigner(t)}
	srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{signer: signer, name: "before"})
	c := startE2EClient(t, transports[0], srv.addr, pki.fixedAnchor(t), attestedCaps)
	c.requireReceived(t, "before")

	signer.useB.Store(true)
	srv.send(t, &protobufs.ServerToAgent{RemoteConfig: remoteConfig("after")})
	c.requireReceived(t, "after")
	assert.Equal(t, int32(1), srv.connects.Load(), "rotation must not force a reconnect")
	assert.Zero(t, c.logger.attestationFailures.Load())
}

// A server whose signer fails drops the connection; the client backs off
// instead of reconnecting in a tight loop.
func TestAttestationE2ESignerFailureBacksOff(t *testing.T) {
	pki := newE2EPKI(t)
	srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{signer: failingSigner{}})
	c := startE2EClient(t, transports[0], srv.addr, pki.fixedAnchor(t), attestedCaps)

	require.Eventually(t, func() bool { return srv.connects.Load() >= 2 }, e2eTimeout, 10*time.Millisecond)
	requireBackoff(t, srv.connects.Load, 2*time.Second, 20)
	assert.False(t, c.received("server"))
	srv.mu.Lock()
	defer srv.mu.Unlock()
	require.NotEmpty(t, srv.sendErrs)
	assert.ErrorContains(t, srv.sendErrs[0], "signing backend unavailable")
}

// Over HTTP, no-op polls are answered with unsigned heartbeats, which the
// client accepts before a later signed message.
func TestAttestationE2EHTTPHeartbeatExemption(t *testing.T) {
	pki := newE2EPKI(t)
	srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{
		signer: pki.signer,
		onMessage: func(n int32, _ *protobufs.AgentToServer) *protobufs.ServerToAgent {
			if n <= 3 {
				return nil
			}
			return &protobufs.ServerToAgent{RemoteConfig: remoteConfig("late")}
		},
	})
	c := startE2EClient(t, transports[1], srv.addr, pki.fixedAnchor(t), attestedCaps)

	c.requireReceived(t, "late")
	assert.GreaterOrEqual(t, srv.messages.Load(), int32(4))
	assert.Zero(t, c.logger.attestationFailures.Load())
}

// A server with a signer still serves Agents that do not require attestation.
func TestAttestationE2EAgentWithoutAttestation(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			pki := newE2EPKI(t)
			srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{signer: pki.signer})
			caps := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
				protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig
			c := startE2EClient(t, tr, srv.addr, nil, caps)

			c.requireReceived(t, "server")
			assert.False(t, srv.sawRequires.Load())
		})
	}
}

func TestAttestationE2ESendBeforeNegotiation(t *testing.T) {
	pki := newE2EPKI(t)
	sendErr := make(chan error, 1)
	srv := startE2EServer(t, testhelpers.GetAvailableLocalAddress(), e2eServerOpts{
		signer: pki.signer,
		onConnected: func(ctx context.Context, conn servertypes.Connection) {
			select {
			case sendErr <- conn.Send(ctx, &protobufs.ServerToAgent{RemoteConfig: remoteConfig("early")}):
			default:
			}
		},
	})
	c := startE2EClient(t, transports[0], srv.addr, pki.fixedAnchor(t), attestedCaps)

	select {
	case err := <-sendErr:
		assert.ErrorIs(t, err, server.ErrSendBeforeNegotiated)
	case <-time.After(e2eTimeout):
		t.Fatal("OnConnected was not called")
	}
	c.requireReceived(t, "server")
	assert.False(t, c.received("early"))
}
