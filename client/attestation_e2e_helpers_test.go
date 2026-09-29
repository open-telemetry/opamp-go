package client

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	servertypes "github.com/open-telemetry/opamp-go/server/types"
	"github.com/open-telemetry/opamp-go/signing"
)

const e2eTimeout = 5 * time.Second

var attestedCaps = protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
	protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
	protobufs.AgentCapabilities_AgentCapabilities_RequiresPayloadTrustVerification

var tofuCaps = attestedCaps | protobufs.AgentCapabilities_AgentCapabilities_AcceptsPayloadTrustAnchorTOFU

// e2ePKI is a CA plus a signing leaf issued by it.
type e2ePKI struct {
	ca     *x509.Certificate
	caKey  crypto.Signer
	caPEM  []byte
	signer *signing.LocalSigner
}

func newE2EPKI(t *testing.T, dnsNames ...string) *e2ePKI {
	t.Helper()
	ca, caKey, err := signing.GenerateCA(signing.AlgorithmECDSAP256SHA256, signing.CertOptions{})
	require.NoError(t, err)
	p := &e2ePKI{ca: ca, caKey: caKey, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})}
	p.signer = p.newSigner(t, dnsNames...)
	return p
}

// newSigner issues a fresh leaf under p's CA.
func (p *e2ePKI) newSigner(t *testing.T, dnsNames ...string) *signing.LocalSigner {
	t.Helper()
	if len(dnsNames) == 0 {
		dnsNames = []string{"localhost"}
	}
	leaf, key, err := signing.GenerateLeaf(signing.AlgorithmECDSAP256SHA256, p.ca, p.caKey, signing.CertOptions{DNSNames: dnsNames})
	require.NoError(t, err)
	s, err := signing.NewLocalSigner(key, []*x509.Certificate{leaf})
	require.NoError(t, err)
	return s.WithRootCA(p.ca)
}

func (p *e2ePKI) fixedAnchor(t *testing.T) signing.PayloadTrustProvider {
	t.Helper()
	v, err := signing.VerifierFromPEM(p.caPEM)
	require.NoError(t, err)
	return signing.FixedAnchor(v)
}

func remoteConfig(name string) *protobufs.AgentRemoteConfig {
	return &protobufs.AgentRemoteConfig{
		Config: &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{
			"": {Body: []byte(name)},
		}},
		ConfigHash: []byte(name),
	}
}

// rotatingSigner delegates to one of two signers, switchable at runtime.
type rotatingSigner struct {
	a, b signing.Signer
	useB atomic.Bool
}

func (r *rotatingSigner) Sign(ctx context.Context, payload []byte) (signing.SignResult, error) {
	if r.useB.Load() {
		return r.b.Sign(ctx, payload)
	}
	return r.a.Sign(ctx, payload)
}

type failingSigner struct{}

func (failingSigner) Sign(context.Context, []byte) (signing.SignResult, error) {
	return signing.SignResult{}, errors.New("signing backend unavailable")
}

// e2eServer is a real OpAMP server with recording callbacks.
type e2eServer struct {
	srv      server.OpAMPServer
	addr     string
	connects atomic.Int32
	messages atomic.Int32
	// sawRequires records whether any AgentToServer declared RequiresPayloadTrustVerification.
	sawRequires atomic.Bool

	mu       sync.Mutex
	conn     servertypes.Connection
	lastUID  []byte
	sendErrs []error
}

type e2eServerOpts struct {
	signer signing.Signer
	// onMessage builds the response; nil returns a RemoteConfig named after the server.
	onMessage   func(n int32, msg *protobufs.AgentToServer) *protobufs.ServerToAgent
	onConnected func(ctx context.Context, conn servertypes.Connection)
	name        string
}

func startE2EServer(t *testing.T, addr string, opts e2eServerOpts) *e2eServer {
	t.Helper()
	s := &e2eServer{addr: addr}
	if opts.name == "" {
		opts.name = "server"
	}
	callbacks := servertypes.Callbacks{
		OnConnecting: func(_ *http.Request) servertypes.ConnectionResponse {
			s.connects.Add(1)
			return servertypes.ConnectionResponse{
				Accept: true,
				ConnectionCallbacks: servertypes.ConnectionCallbacks{
					OnConnected: func(ctx context.Context, conn servertypes.Connection) {
						if opts.onConnected != nil {
							opts.onConnected(ctx, conn)
						}
					},
					OnMessage: func(_ context.Context, conn servertypes.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
						n := s.messages.Add(1)
						if msg.Capabilities&uint64(protobufs.AgentCapabilities_AgentCapabilities_RequiresPayloadTrustVerification) != 0 {
							s.sawRequires.Store(true)
						}
						s.mu.Lock()
						s.conn, s.lastUID = conn, msg.InstanceUid
						s.mu.Unlock()
						if opts.onMessage != nil {
							return opts.onMessage(n, msg)
						}
						return &protobufs.ServerToAgent{RemoteConfig: remoteConfig(opts.name)}
					},
					OnMessageResponseError: func(_ servertypes.Connection, _ *protobufs.ServerToAgent, err error) {
						s.mu.Lock()
						s.sendErrs = append(s.sendErrs, err)
						s.mu.Unlock()
					},
				},
			}
		},
	}
	s.srv = server.New(nil)
	require.NoError(t, s.srv.Start(server.StartSettings{
		Settings:       server.Settings{Callbacks: callbacks, PayloadSigner: opts.signer},
		ListenEndpoint: addr,
		ListenPath:     "/v1/opamp",
	}))
	t.Cleanup(func() { s.stop() })
	testhelpers.WaitForEndpoint(addr)
	return s
}

func (s *e2eServer) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()
	_ = s.srv.Stop(ctx)
	s.disconnect()
}

func (s *e2eServer) disconnect() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Disconnect()
	}
}

func (s *e2eServer) send(t *testing.T, msg *protobufs.ServerToAgent) {
	t.Helper()
	s.mu.Lock()
	conn, uid := s.conn, s.lastUID
	s.mu.Unlock()
	require.NotNil(t, conn)
	msg.InstanceUid = uid
	require.NoError(t, conn.Send(context.Background(), msg))
}

// recordingLogger counts attestation failures reported by the client.
type recordingLogger struct {
	attestationFailures atomic.Int32
}

func (l *recordingLogger) Debugf(context.Context, string, ...interface{}) {}

func (l *recordingLogger) Errorf(_ context.Context, format string, v ...interface{}) {
	if strings.Contains(fmt.Sprintf(format, v...), "Payload trust verification failed") {
		l.attestationFailures.Add(1)
	}
}

// e2eClient is a started OpAMP client recording the RemoteConfigs it accepts.
type e2eClient struct {
	OpAMPClient
	logger   *recordingLogger
	stopOnce sync.Once

	mu      sync.Mutex
	configs []string
}

type transport struct {
	name   string
	scheme string
	new    func(types.Logger) OpAMPClient
}

var transports = []transport{
	{"ws", "ws", func(l types.Logger) OpAMPClient { return NewWebSocket(l) }},
	{"http", "http", func(l types.Logger) OpAMPClient { return NewHTTP(l) }},
}

func startE2EClient(t *testing.T, tr transport, addr string, provider signing.PayloadTrustProvider, caps protobufs.AgentCapabilities) *e2eClient {
	t.Helper()
	c := &e2eClient{logger: &recordingLogger{}}
	c.OpAMPClient = tr.new(c.logger)
	if hc, ok := c.OpAMPClient.(*httpClient); ok {
		hc.SetPollingInterval(50 * time.Millisecond)
	}
	require.NoError(t, c.SetAgentDescription(createAgentDescr()))
	require.NoError(t, c.SetCapabilities(&caps))
	settings := types.StartSettings{
		OpAMPServerURL:       tr.scheme + "://" + strings.Replace(addr, "127.0.0.1", "localhost", 1) + "/v1/opamp",
		InstanceUid:          genNewInstanceUid(t),
		PayloadTrustProvider: provider,
		Callbacks: types.Callbacks{
			OnMessage: func(_ context.Context, msg *types.MessageData) {
				if msg.RemoteConfig != nil {
					c.mu.Lock()
					c.configs = append(c.configs, string(msg.RemoteConfig.ConfigHash))
					c.mu.Unlock()
				}
			},
		},
	}
	require.NoError(t, c.Start(context.Background(), settings))
	t.Cleanup(c.stop)
	return c
}

// stop stops the client once; a second ClientCommon.Stop would block forever.
func (c *e2eClient) stop() {
	c.stopOnce.Do(func() { _ = c.Stop(context.Background()) })
}

func (c *e2eClient) received(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.configs {
		if n == name {
			return true
		}
	}
	return false
}

func (c *e2eClient) configCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.configs)
}

func (c *e2eClient) requireReceived(t *testing.T, name string) {
	t.Helper()
	require.Eventually(t, func() bool { return c.received(name) }, e2eTimeout, 10*time.Millisecond,
		"client did not accept RemoteConfig %q", name)
}

// countingStore wraps a TOFUStore and counts Save calls.
type countingStore struct {
	signing.TOFUStore
	saves atomic.Int32
}

func (s *countingStore) Save(pemBytes []byte) error {
	s.saves.Add(1)
	return s.TOFUStore.Save(pemBytes)
}
