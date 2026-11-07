package opampsrv

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
)

type testOpAMPServer struct{ server.OpAMPServer }

func (s testOpAMPServer) Start(settings server.StartSettings) error {
	settings.ListenEndpoint = "127.0.0.1:0"
	return s.OpAMPServer.Start(settings)
}

func startTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv := NewServer(&data.AllAgents, false)
	srv.logger = &Logger{log.New(io.Discard, "", 0)}
	srv.opampSrv = testOpAMPServer{srv.opampSrv}
	srv.Start(true)
	t.Cleanup(srv.Stop)
	return srv, "http://" + srv.opampSrv.Addr().String() + "/v1/opamp"
}

func poll(t *testing.T, client *http.Client, endpoint string, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	t.Helper()
	body, err := proto.Marshal(msg)
	require.NoError(t, err)
	resp, err := client.Post(endpoint, "application/x-protobuf", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	result := &protobufs.ServerToAgent{}
	require.NoError(t, proto.Unmarshal(body, result))
	return result
}

func TestHTTPAgentPersistsBetweenPolls(t *testing.T) {
	srv, endpoint := startTestServer(t)
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	id := data.InstanceId([]byte("http-agent-state"))
	t.Cleanup(func() { srv.agents.RemoveHTTPAgent(id) })
	msg := &protobufs.AgentToServer{
		InstanceUid: id[:], SequenceNum: 1,
		AgentDescription: &protobufs.AgentDescription{IdentifyingAttributes: []*protobufs.KeyValue{{Key: "service.name", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "http-service"}}}}},
		Health:           &protobufs.ComponentHealth{Healthy: true},
	}
	response := poll(t, client, endpoint, msg)
	assert.Equal(t, serverCapabilities, response.Capabilities)
	agent := srv.agents.GetAgentReadonlyClone(id)
	require.NotNil(t, agent, "HTTP agent must remain visible after its request closes")
	assert.Equal(t, "http-service", agent.ServiceName())
	assert.Equal(t, "Up", agent.HealthStatus())

	response = poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 2})
	assert.Equal(t, serverCapabilities, response.Capabilities)
	agent = srv.agents.GetAgentReadonlyClone(id)
	require.NotNil(t, agent)
	assert.Equal(t, "http-service", agent.ServiceName())
	assert.Equal(t, "Up", agent.HealthStatus())
	assert.Equal(t, uint64(2), agent.Status.SequenceNum)
	assert.Len(t, srv.agents.GetAllAgentsReadonlyClone(), 1)

	poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 3, AgentDisconnect: &protobufs.AgentDisconnect{}})
	assert.Nil(t, srv.agents.FindAgent(id))
}

func TestHTTPAgentReceivesUIOperations(t *testing.T) {
	srv, endpoint := startTestServer(t)
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	id := data.InstanceId([]byte("http-agent-uiops"))
	otherID := data.InstanceId([]byte("http-agent-other"))
	for _, agentID := range []data.InstanceId{id, otherID} {
		t.Cleanup(func() { srv.agents.RemoveHTTPAgent(agentID) })
		poll(t, client, endpoint, &protobufs.AgentToServer{
			InstanceUid: agentID[:], SequenceNum: 1,
			Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig),
		})
	}

	notify := make(chan struct{}, 1)
	config := &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{"": {Body: []byte("http-config")}}}
	srv.agents.SetCustomConfigForAgent(id, config, notify)
	offers := &protobufs.ConnectionSettingsOffers{Opamp: &protobufs.OpAMPConnectionSettings{DestinationEndpoint: "https://localhost:4320/v1/opamp"}}
	srv.agents.OfferAgentConnectionSettings(id, offers)
	for _, body := range []string{"first", "second"} {
		srv.agents.SendCustomMessageToAgent(id, &protobufs.ServerToAgent{CustomMessage: &protobufs.CustomMessage{Capability: "example.test", Type: "test", Data: []byte(body)}})
	}

	response := poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: otherID[:], SequenceNum: 2})
	assert.Nil(t, response.RemoteConfig)
	assert.Nil(t, response.ConnectionSettings)
	assert.Nil(t, response.CustomMessage)
	select {
	case <-notify:
		t.Fatal("another Agent's poll must not notify config watchers")
	default:
	}

	response = poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 2})
	require.NotNil(t, response.RemoteConfig)
	assert.True(t, proto.Equal(config, response.RemoteConfig.Config))
	assert.Equal(t, "http-config", srv.agents.GetAgentReadonlyClone(id).CustomInstanceConfig)
	select {
	case <-notify:
		t.Fatal("the config delivery poll must not notify its watcher")
	default:
	}

	response = poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 3})
	assert.True(t, proto.Equal(offers, response.ConnectionSettings))
	select {
	case <-notify:
	default:
		t.Fatal("expected the status update after config delivery to notify its watcher")
	}
	for i, body := range []string{"first", "second"} {
		response = poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: uint64(i + 4)})
		require.NotNil(t, response.CustomMessage)
		assert.Equal(t, body, string(response.CustomMessage.Data))
	}
	response = poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 6})
	assert.Nil(t, response.RemoteConfig)
	assert.Nil(t, response.ConnectionSettings)
	assert.Nil(t, response.CustomMessage)
}

func TestHTTPQueuedSettingsSurviveCertificateRequest(t *testing.T) {
	srv, endpoint := startTestServer(t)
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	id := data.InstanceId([]byte("http-csr-agent01"))
	t.Cleanup(func() { srv.agents.RemoveHTTPAgent(id) })
	poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 1})
	offer := &protobufs.ConnectionSettingsOffers{Opamp: &protobufs.OpAMPConnectionSettings{
		Tls:   &protobufs.TLSConnectionSettings{MinVersion: "1.2", MaxVersion: "1.3"},
		Proxy: &protobufs.ProxyConnectionSettings{Url: "http://proxy.example:8080"},
	}}
	srv.agents.OfferAgentConnectionSettings(id, offer)
	message := &protobufs.CustomMessage{Capability: "example.test", Type: "after-settings"}
	srv.agents.SendCustomMessageToAgent(id, &protobufs.ServerToAgent{CustomMessage: message})

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "OpAMP Example Client"},
	}, key)
	require.NoError(t, err)
	for seq := uint64(2); seq <= 3; seq++ {
		response := poll(t, client, endpoint, &protobufs.AgentToServer{
			InstanceUid: id[:], SequenceNum: seq,
			ConnectionSettingsRequest: &protobufs.ConnectionSettingsRequest{Opamp: &protobufs.OpAMPConnectionSettingsRequest{
				CertificateRequest: &protobufs.CertificateRequest{Csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})},
			}},
		})
		require.Nil(t, response.ErrorResponse)
		require.NotNil(t, response.GetConnectionSettings().GetOpamp().GetCertificate())
		assert.Nil(t, response.GetConnectionSettings().GetOpamp().GetTls())
		assert.Nil(t, response.CustomMessage, "later messages must stay behind the deferred offer")
	}

	response := poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 4})
	assert.True(t, proto.Equal(offer, response.ConnectionSettings), "deliver the whole offer with its original hash")
	assert.Nil(t, response.CustomMessage)
	response = poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 5})
	assert.Nil(t, response.ConnectionSettings)
	assert.True(t, proto.Equal(message, response.CustomMessage))
	response = poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 6})
	assert.Nil(t, response.ConnectionSettings)
	assert.Nil(t, response.CustomMessage)
}

func TestHTTPConfigWatcherWaitsForStatusAfterDelivery(t *testing.T) {
	srv, endpoint := startTestServer(t)
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	id := data.InstanceId([]byte("http-configwait1"))
	t.Cleanup(func() { srv.agents.RemoveHTTPAgent(id) })
	poll(t, client, endpoint, &protobufs.AgentToServer{
		InstanceUid: id[:], SequenceNum: 1,
		Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig),
	})
	srv.agents.SendCustomMessageToAgent(id, &protobufs.ServerToAgent{CustomMessage: &protobufs.CustomMessage{Capability: "example.test", Type: "before-config"}})
	notifications := []chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}
	for i, body := range []string{"first-config", "second-config"} {
		srv.agents.SetCustomConfigForAgent(id, &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigObject{"": {Body: []byte(body)}},
		}, notifications[i])
	}
	assertQuiet := func(ch chan struct{}) {
		t.Helper()
		select {
		case <-ch:
			t.Fatal("config watcher fired before the status update after its delivery")
		default:
		}
	}
	response := poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 2})
	require.NotNil(t, response.CustomMessage)
	require.Nil(t, response.RemoteConfig)
	for _, ch := range notifications {
		assertQuiet(ch)
	}

	var configStatus *protobufs.RemoteConfigStatus
	for i, body := range []string{"first-config", "second-config"} {
		response = poll(t, client, endpoint, &protobufs.AgentToServer{
			InstanceUid: id[:], SequenceNum: uint64(i + 3), RemoteConfigStatus: configStatus,
		})
		require.NotNil(t, response.RemoteConfig)
		assert.Equal(t, body, string(response.RemoteConfig.Config.ConfigMap[""].Body))
		assert.Nil(t, response.CustomMessage)
		if i > 0 {
			require.Len(t, notifications[i-1], 1, "the preceding config's watcher must fire")
			<-notifications[i-1]
		}
		for _, ch := range notifications {
			assertQuiet(ch)
		}
		configStatus = &protobufs.RemoteConfigStatus{
			LastRemoteConfigHash: response.RemoteConfig.ConfigHash,
			Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		}
	}
	response = poll(t, client, endpoint, &protobufs.AgentToServer{
		InstanceUid: id[:], SequenceNum: 5, RemoteConfigStatus: configStatus,
	})
	assert.Nil(t, response.RemoteConfig)
	assert.Nil(t, response.CustomMessage)
	require.Len(t, notifications[1], 1)
	<-notifications[1]
	poll(t, client, endpoint, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 6})
	for _, ch := range notifications {
		assertQuiet(ch)
	}
}

func TestWebSocketAgentStillReceivesPushAndDisconnects(t *testing.T) {
	srv, endpoint := startTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(endpoint, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	id := data.InstanceId([]byte("websocket-agent1"))
	require.NoError(t, internal.WriteWSMessage(conn, &protobufs.AgentToServer{InstanceUid: id[:], SequenceNum: 1}, -1))
	read := func() *protobufs.ServerToAgent {
		t.Helper()
		_, body, err := conn.ReadMessage()
		require.NoError(t, err)
		response := &protobufs.ServerToAgent{}
		require.NoError(t, internal.DecodeWSMessage(body, response))
		return response
	}
	assert.Equal(t, serverCapabilities, read().Capabilities)
	require.NotNil(t, srv.agents.FindAgent(id))
	message := &protobufs.ServerToAgent{CustomMessage: &protobufs.CustomMessage{Capability: "example.test", Type: "push", Data: []byte("hello")}}
	srv.agents.SendCustomMessageToAgent(id, message)
	assert.True(t, proto.Equal(message.CustomMessage, read().CustomMessage))
	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool { return srv.agents.FindAgent(id) == nil }, time.Second, time.Millisecond)
}
