// Package apisrv provides a REST API for the OpAMP Server example.
//
// This package implements HTTP endpoints that allow programmatic access to
// connected agents, enabling external clients (web UIs, CLI tools, etc.) to
// interact with the OpAMP server without tight coupling to the UI layer.
//
// The API provides endpoints for:
//   - Listing all connected agents
//   - Retrieving individual agent details
//   - Updating agent configurations
//
// All responses are in JSON format and include proper error handling with
// appropriate HTTP status codes.
package apisrv

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// ApiServer is the HTTP server that provides REST API access to OpAMP agents.
// It runs on port 4322 and provides endpoints for listing agents, retrieving
// agent details, and updating agent configurations.
type ApiServer struct {
	srv    *http.Server
	logger *log.Logger

	agents *data.Agents
}

// NewApiServer creates a new API server instance.
//
// Parameters:
//   - agents: Reference to the shared agents manager that tracks connected agents
//   - logger: Logger for API server events and errors
//
// Returns a configured ApiServer ready to be started with Start().
func NewApiServer(agents *data.Agents, logger *log.Logger) *ApiServer {
	return &ApiServer{
		agents: agents,
		logger: logger,
	}
}

// AgentList represents the response for the GET /api/v1/agents endpoint.
// It contains an array of all currently connected agents.
type AgentList struct {
	Data []Agent `json:"data"`
}

// Agent represents a single agent in API responses.
// It includes the server-assigned UUID and the full agent status as reported
// via the OpAMP protocol.
type Agent struct {
	// UUID is the server-assigned unique identifier for this agent connection
	UUID string `json:"uuid"`
	// Status contains the full AgentToServer protobuf message with agent details,
	// capabilities, health, configuration, and other OpAMP protocol fields
	Status *protobufs.AgentToServer `json:"status"`
}

// agentsHandler handles GET /api/v1/agents requests.
//
// Returns a JSON array of all agents currently connected to the OpAMP server.
// Each agent includes its UUID and full status information.
//
// Response: 200 OK with AgentList JSON
func (s *ApiServer) agentsHandler(w http.ResponseWriter, r *http.Request) {
	var agent_list AgentList

	for _, value := range s.agents.GetAllAgentsReadonlyClone() {
		agent_list.Data = append(agent_list.Data, Agent{
			UUID:   value.InstanceIdStr,
			Status: value.Status,
		})
	}

	s.writeJSON(w, http.StatusOK, agent_list)
}

// agentHandler handles GET /api/v1/agents/{instanceid} requests.
//
// Retrieves detailed information about a specific agent by its instance ID.
//
// Path parameters:
//   - instanceid: UUID of the agent instance
//
// Responses:
//   - 200 OK: Agent found, returns Agent JSON
//   - 400 Bad Request: Invalid UUID format
//   - 404 Not Found: Agent not found
func (s *ApiServer) agentHandler(w http.ResponseWriter, r *http.Request) {
	instanceIDStr := r.PathValue("instanceid")

	uid, err := uuid.Parse(instanceIDStr)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid instance ID format",
		})
		return
	}

	agent := s.agents.GetAgentReadonlyClone(data.InstanceId(uid))
	if agent == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Agent not found",
		})
		return
	}

	s.writeJSON(w, http.StatusOK, Agent{
		UUID:   agent.InstanceIdStr,
		Status: agent.Status,
	})
}

// ConfigRequest represents the request body for updating an agent's configuration.
// The config field should contain the configuration content, typically in YAML format.
type ConfigRequest struct {
	// Config is the configuration content to send to the agent
	Config string `json:"config"`
}

// updateAgentConfigHandler handles POST /api/v1/agents/{instanceid}/config requests.
//
// Updates the configuration for a specific agent. The server sends the new
// configuration to the agent via OpAMP protocol and waits up to 5 seconds for
// the agent to acknowledge receipt.
//
// Path parameters:
//   - instanceid: UUID of the agent instance
//
// Request body: JSON with "config" field containing the configuration content
//
// Responses:
//   - 200 OK: Configuration updated and acknowledged by agent
//   - 400 Bad Request: Invalid UUID format, invalid JSON, or failed to read body
//   - 404 Not Found: Agent not found
//   - 408 Request Timeout: Agent didn't acknowledge within 5 seconds
func (s *ApiServer) updateAgentConfigHandler(w http.ResponseWriter, r *http.Request) {
	instanceIDStr := r.PathValue("instanceid")

	uid, err := uuid.Parse(instanceIDStr)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid instance ID format",
		})
		return
	}

	instanceId := data.InstanceId(uid)
	agent := s.agents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Agent not found",
		})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Failed to read request body",
		})
		return
	}
	defer r.Body.Close()

	var configReq ConfigRequest
	err = json.Unmarshal(body, &configReq)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON format",
		})
		return
	}

	config := &protobufs.AgentConfigMap{
		ConfigMap: map[string]*protobufs.AgentConfigFile{
			"": {Body: []byte(configReq.Config)},
		},
	}

	notifyNextStatusUpdate := make(chan struct{}, 1)
	s.agents.SetCustomConfigForAgent(instanceId, config, notifyNextStatusUpdate)

	// Wait for up to 5 seconds for the agent to acknowledge the configuration update
	timer := time.NewTimer(time.Second * 5)
	defer timer.Stop()

	select {
	case <-notifyNextStatusUpdate:
		s.logger.Printf("Agent %s acknowledged config update\n", instanceId)
		s.writeJSON(w, http.StatusOK, map[string]string{
			"message": "Configuration updated successfully",
		})
	case <-timer.C:
		s.logger.Printf("Timeout waiting for agent %s to acknowledge config update\n", instanceId)
		s.writeJSON(w, http.StatusRequestTimeout, map[string]string{
			"error": "Timeout waiting for agent to acknowledge configuration update",
		})
	}
}

// writeJSON writes arbitrary data out as JSON with the specified HTTP status code.
//
// This helper method marshals the data to JSON, sets the Content-Type header,
// and writes the response. Any marshaling or write errors are logged.
//
// Parameters:
//   - w: HTTP response writer
//   - status: HTTP status code to return
//   - data: Data to marshal as JSON
//   - headers: Optional additional headers to include in the response
func (s *ApiServer) writeJSON(w http.ResponseWriter, status int, data interface{}, headers ...http.Header) {
	out, err := json.Marshal(data)
	if err != nil {
		s.logger.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	if len(headers) > 0 {
		for k, v := range headers[0] {
			w.Header()[k] = v
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(out)
	if err != nil {
		s.logger.Println(err)
	}
}

// corsMiddleware adds CORS headers to allow cross-origin requests.
//
// This middleware enables web applications hosted on different domains to
// interact with the API. It handles preflight OPTIONS requests and adds
// appropriate CORS headers to all responses.
//
// CORS configuration:
//   - Allows all origins (*)
//   - Allows GET, POST, PUT, DELETE, OPTIONS methods
//   - Allows Content-Type and Authorization headers
//   - Preflight cache: 3600 seconds
//
// Note: In production, consider restricting allowed origins for security.
func (s *ApiServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow requests from any origin
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "3600")

		// Handle preflight OPTIONS requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Start initializes and starts the API server on port 4322.
//
// The server runs in a separate goroutine and provides the following endpoints:
//   - GET /api/v1/agents - List all connected agents
//   - GET /api/v1/agents/{instanceid} - Get specific agent details
//   - POST /api/v1/agents/{instanceid}/config - Update agent configuration
//
// All endpoints include CORS support for cross-origin requests.
// The server continues running until Shutdown() is called.
func (s *ApiServer) Start() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/agents", s.agentsHandler)
	mux.HandleFunc("GET /api/v1/agents/{instanceid}", s.agentHandler)
	mux.HandleFunc("POST /api/v1/agents/{instanceid}/config", s.updateAgentConfigHandler)

	// Wrap the mux with CORS middleware to enable cross-origin requests
	handler := s.corsMiddleware(mux)

	s.srv = &http.Server{
		Addr:    "0.0.0.0:4322",
		Handler: handler,
	}
	go func() {
		s.logger.Println("Starting API server on", s.srv.Addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Println("API server error:", err)
		}
	}()
}

// Shutdown gracefully stops the API server.
//
// This method blocks until the server has finished shutting down or the
// context is canceled. It should be called during application shutdown
// to ensure clean termination of the HTTP server.
func (s *ApiServer) Shutdown() {
	s.srv.Shutdown(context.Background())
}
