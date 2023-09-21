package uisrv

import (
	"context"
	"log"
	"net/http"
	"path"
	"text/template"
	"time"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var htmlDir string
var srv *http.Server

var logger = log.New(log.Default().Writer(), "[UI] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

func Start(rootDir string) {
	htmlDir = path.Join(rootDir, "uisrv/html")

	mux := http.NewServeMux()
	mux.HandleFunc("/", renderRoot)
	mux.HandleFunc("/agent", renderAgent)
	mux.HandleFunc("/save_config", saveCustomConfigForInstance)
	mux.HandleFunc("/rotate_client_cert", rotateInstanceClientCert)
	srv = &http.Server{
		Addr:    "0.0.0.0:4321",
		Handler: mux,
	}
	go srv.ListenAndServe()
}

func Shutdown() {
	srv.Shutdown(context.Background())
}

func renderTemplate(w http.ResponseWriter, htmlTemplateFile string, data interface{}) {
	t, err := template.ParseFiles(
		path.Join(htmlDir, "header.html"),
		path.Join(htmlDir, htmlTemplateFile),
	)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logger.Printf("Error parsing html template %s: %v", htmlTemplateFile, err)
		return
	}

	err = t.Lookup(htmlTemplateFile).Execute(w, data)
	if err != nil {
		// It is too late to send an HTTP status code since content is already written.
		// We can just log the error.
		logger.Printf("Error writing html content %s: %v", htmlTemplateFile, err)
		return
	}
}

func renderRoot(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "root.html", data.AllAgents.GetAllAgentsReadonlyClone())
}

func renderAgent(w http.ResponseWriter, r *http.Request) {
	agent := data.AllAgents.GetAgentReadonlyClone(data.InstanceId(r.URL.Query().Get("instanceid")))
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	renderTemplate(w, "agent.html", agent)
}

func saveCustomConfigForInstance(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	instanceId := data.InstanceId(r.Form.Get("instanceid"))
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	configStr := r.PostForm.Get("config")
	config := &protobufs.AgentConfigMap{
		ConfigMap: map[string]*protobufs.AgentConfigFile{
			"": {Body: []byte(configStr)},
		},
	}

	notifyNextStatusUpdate := make(chan struct{}, 1)
	data.AllAgents.SetCustomConfigForAgent(instanceId, config, notifyNextStatusUpdate)

	// Wait for up to 5 seconds for a Status update, which is expected
	// to be reported by the Agent after we set the remote config.
	timer := time.NewTicker(time.Second * 5)

	select {
	case <-notifyNextStatusUpdate:
	case <-timer.C:
	}

	http.Redirect(w, r, "/agent?instanceid="+string(instanceId), http.StatusSeeOther)
}

func rotateInstanceClientCert(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Find the agent instance.
	instanceId := data.InstanceId(r.Form.Get("instanceid"))
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Create a new certificate for the agent.
	certificate, err := internal.CreateTLSCert("../../certs/certs/ca.cert.pem", "../../certs/certs/ca.key.pem")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logger.Println(err)
		return
	}

	// Create an offer for the agent.
	offers := &protobufs.ConnectionSettingsOffers{
		Opamp: &protobufs.OpAMPConnectionSettings{
			Certificate: certificate,
		},
	}

	// Send the offer to the agent.
	data.AllAgents.OfferAgentConnectionSettings(instanceId, offers)

	logger.Printf("Waiting for agent %s to reconnect\n", instanceId)

	// Wait for up to 5 seconds for a Status update, which is expected
	// to be reported by the agent after we set the remote config.
	timer := time.NewTicker(time.Second * 5)

	// TODO: wait for agent to reconnect instead of waiting full 5 seconds.

	select {
	case <-timer.C:
		logger.Printf("Time out waiting for agent %s to reconnect\n", instanceId)
	}

	http.Redirect(w, r, "/agent?instanceid="+string(instanceId), http.StatusSeeOther)
}
