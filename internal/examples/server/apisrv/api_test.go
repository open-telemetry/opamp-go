package apisrv

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/configtls"

	"github.com/open-telemetry/opamp-go/internal/examples/agent/agent"
	"github.com/open-telemetry/opamp-go/internal/examples/config"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/internal/examples/server/opampsrv"
)

type TestServer struct {
	Agents   *data.Agents
	OpampSrv *opampsrv.Server
	ApiSrv   *ApiServer
}

func NewTestServer(t *testing.T) *TestServer {
	t.Helper()

	agents := &data.AllAgents

	opampSrv := opampsrv.NewServer(agents)
	opampSrv.Start()

	logger := log.New(io.Discard, "", 0)
	apiSrv := NewApiServer(agents, logger)
	apiSrv.Start()

	time.Sleep(200 * time.Millisecond)

	t.Cleanup(func() {
		apiSrv.Shutdown()
		opampSrv.Stop()
	})

	return &TestServer{
		Agents:   agents,
		OpampSrv: opampSrv,
		ApiSrv:   apiSrv,
	}
}

type TestAgent struct {
	Agent *agent.Agent
}

func NewTestAgent(t *testing.T) *TestAgent {
	t.Helper()

	agentConfig := &config.AgentConfig{
		Endpoint: "wss://127.0.0.1:4320/v1/opamp",
		TLSSetting: configtls.ClientConfig{
			InsecureSkipVerify: true,
		},
	}

	agentLogger := &agent.Logger{Logger: log.New(io.Discard, "", 0)}
	ag := agent.NewAgent(agentLogger, "io.opentelemetry.collector", "1.0.0", agentConfig)
	require.NotNil(t, ag, "Agent should be created successfully")

	ta := &TestAgent{
		Agent: ag,
	}

	t.Cleanup(func() {
		ta.Agent.Shutdown()
	})

	return ta
}

func GetAgentsFromAPI(t *testing.T) map[string]interface{} {
	t.Helper()

	resp, err := http.Get("http://localhost:4322/api/v1/agents")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	require.NoError(t, err)

	return result
}

func ExtractAgentUUIDs(t *testing.T, result map[string]interface{}) map[string]bool {
	t.Helper()

	assert.Contains(t, result, "data")
	dataArray, ok := result["data"].([]interface{})
	require.True(t, ok, "data should be an array")

	apiUUIDs := make(map[string]bool)
	for _, item := range dataArray {
		agentData, ok := item.(map[string]interface{})
		require.True(t, ok, "agent should be an object")
		assert.Contains(t, agentData, "uuid")
		assert.Contains(t, agentData, "status")

		agentUUID, ok := agentData["uuid"].(string)
		require.True(t, ok, "uuid should be a string")
		assert.NotEmpty(t, agentUUID)
		apiUUIDs[agentUUID] = true
	}

	return apiUUIDs
}

func TestServerStartStop(t *testing.T) {
	srv := NewTestServer(t)
	require.NotNil(t, srv)
	require.NotNil(t, srv.Agents)
	require.NotNil(t, srv.OpampSrv)
	require.NotNil(t, srv.ApiSrv)
}

func TestGetAgents(t *testing.T) {
	srv := NewTestServer(t)
	require.NotNil(t, srv)

	testAgent1 := NewTestAgent(t)
	require.NotNil(t, testAgent1)

	testAgent2 := NewTestAgent(t)
	require.NotNil(t, testAgent2)

	// Wait for agents to connect and register with the OpAMP server
	time.Sleep(500 * time.Millisecond)

	response := GetAgentsFromAPI(t)
	apiUUIDs := ExtractAgentUUIDs(t, response)

	// Verify both test agents' UUIDs are in the API response
	expectedUUID1 := testAgent1.Agent.GetInstanceId().String()
	expectedUUID2 := testAgent2.Agent.GetInstanceId().String()

	assert.True(t, apiUUIDs[expectedUUID1], "API should contain first agent's UUID")
	assert.True(t, apiUUIDs[expectedUUID2], "API should contain second agent's UUID")
	assert.NotEqual(t, expectedUUID1, expectedUUID2, "Agent UUIDs should be different")
}
