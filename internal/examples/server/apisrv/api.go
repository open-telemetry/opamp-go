package apisrv

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	var agent_list AgentList

	for _, value := range s.agents.GetAllAgentsReadonlyClone() {
		agent_list.Data = append(agent_list.Data, Agent{
			UUID:   value.InstanceIdStr,
			Status: value.Status,
		})
	}

	s.writeJSON(w, http.StatusOK, agent_list)
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

func (s *ApiServer) Start() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/agents", s.agentsHandler)

	s.srv = &http.Server{
		Addr:    "0.0.0.0:4322",
		Handler: mux,
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
