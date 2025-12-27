package apisrv

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
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

func GetAgentByIDFromAPI(t *testing.T, agentUUID string) (int, map[string]interface{}) {
	t.Helper()

	resp, err := http.Get("http://localhost:4322/api/v1/agents/" + agentUUID)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	require.NoError(t, err)

	return resp.StatusCode, result
}

func ValidateAgentResponse(t *testing.T, result map[string]interface{}, expectedUUID string) {
	t.Helper()

	assert.Contains(t, result, "uuid")
	assert.Contains(t, result, "status")
	assert.Equal(t, expectedUUID, result["uuid"])
}

func ValidateErrorResponse(t *testing.T, result map[string]interface{}) {
	t.Helper()

	assert.Contains(t, result, "error")
}

func UpdateAgentConfigViaAPI(t *testing.T, agentUUID string, config string) (int, map[string]interface{}) {
	t.Helper()

	configRequest := map[string]string{"config": config}
	configJSONBytes, err := json.Marshal(configRequest)
	require.NoError(t, err)

	resp, err := http.Post(
		"http://localhost:4322/api/v1/agents/"+agentUUID+"/config",
		"application/json",
		bytes.NewReader(configJSONBytes),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	require.NoError(t, err)

	return resp.StatusCode, result
}

func ValidateSuccessResponse(t *testing.T, result map[string]interface{}) {
	t.Helper()

	assert.Contains(t, result, "message")
}

func GetAgentConfig(t *testing.T, agents *data.Agents, agentUUID string) string {
	t.Helper()

	uid, err := uuid.Parse(agentUUID)
	require.NoError(t, err)

	agent := agents.GetAgentReadonlyClone(data.InstanceId(uid))
	require.NotNil(t, agent)

	return agent.GetCustomConfig()
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

func TestGetAgentByID(t *testing.T) {
	srv := NewTestServer(t)
	require.NotNil(t, srv)

	testAgent := NewTestAgent(t)
	require.NotNil(t, testAgent)

	// Wait for agent to connect and register with the OpAMP server
	time.Sleep(500 * time.Millisecond)

	agentUUID := testAgent.Agent.GetInstanceId().String()

	// Test successful retrieval
	statusCode, result := GetAgentByIDFromAPI(t, agentUUID)
	assert.Equal(t, http.StatusOK, statusCode)
	ValidateAgentResponse(t, result, agentUUID)

	// Test invalid UUID format
	statusCode, errorResult := GetAgentByIDFromAPI(t, "invalid-uuid")
	assert.Equal(t, http.StatusBadRequest, statusCode)
	ValidateErrorResponse(t, errorResult)

	// Test non-existent agent
	nonExistentUUID := "00000000-0000-0000-0000-000000000000"
	statusCode, errorResult = GetAgentByIDFromAPI(t, nonExistentUUID)
	assert.Equal(t, http.StatusNotFound, statusCode)
	ValidateErrorResponse(t, errorResult)
}

func TestUpdateAgentConfig(t *testing.T) {
	srv := NewTestServer(t)
	require.NotNil(t, srv)

	testAgent := NewTestAgent(t)
	require.NotNil(t, testAgent)

	// Wait for agent to connect and register with the OpAMP server
	time.Sleep(500 * time.Millisecond)

	agentUUID := testAgent.Agent.GetInstanceId().String()

	// Get the config before update
	configBeforeUpdate := GetAgentConfig(t, srv.Agents, agentUUID)

	// Test successful config update - append a comment to the config
	expectedConfig := configBeforeUpdate + "# comment for testing\n"
	statusCode, result := UpdateAgentConfigViaAPI(t, agentUUID, expectedConfig)
	assert.Equal(t, http.StatusOK, statusCode)
	ValidateSuccessResponse(t, result)

	// Verify the agent received the updated config
	configAfterUpdate := GetAgentConfig(t, srv.Agents, agentUUID)
	assert.Equal(t, expectedConfig, configAfterUpdate)
	assert.NotEqual(t, configBeforeUpdate, configAfterUpdate, "Config should have changed")

	// Test invalid UUID format
	statusCode, errorResult := UpdateAgentConfigViaAPI(t, "invalid-uuid", expectedConfig)
	assert.Equal(t, http.StatusBadRequest, statusCode)
	ValidateErrorResponse(t, errorResult)

	// Test non-existent agent
	nonExistentUUID := "00000000-0000-0000-0000-000000000000"
	statusCode, errorResult = UpdateAgentConfigViaAPI(t, nonExistentUUID, expectedConfig)
	assert.Equal(t, http.StatusNotFound, statusCode)
	ValidateErrorResponse(t, errorResult)
}
