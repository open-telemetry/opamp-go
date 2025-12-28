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

type ApiServer struct {
	srv    *http.Server
	logger *log.Logger

	agents *data.Agents
}

func NewApiServer(agents *data.Agents, logger *log.Logger) *ApiServer {
	return &ApiServer{
		agents: agents,
		logger: logger,
	}
}

type AgentList struct {
	Data []Agent `json:"data"`
}

type Agent struct {
	UUID   string                   `json:"uuid"`
	Status *protobufs.AgentToServer `json:"status"`
}

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

type ConfigRequest struct {
	Config string `json:"config"`
}

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

	// Wait for up to 5 seconds for a Status update
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

// writeJSON writes arbitrary data out as JSON
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

// corsMiddleware adds CORS headers to allow cross-origin requests
func (s *ApiServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow requests from any origin (adjust as needed for production)
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

func (s *ApiServer) Start() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/agents", s.agentsHandler)
	mux.HandleFunc("GET /api/v1/agents/{instanceid}", s.agentHandler)
	mux.HandleFunc("POST /api/v1/agents/{instanceid}/config", s.updateAgentConfigHandler)

	// Wrap mux with CORS middleware
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

func (s *ApiServer) Shutdown() {
	s.srv.Shutdown(context.Background())
}
