package uisrv

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"path"
	"text/template"
	"time"

	"github.com/google/uuid"

	"github.com/open-telemetry/opamp-go/internal/examples/certs"
	"github.com/open-telemetry/opamp-go/internal/examples/html"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var (
	htmlDir string
	srv     *http.Server
)

var logger = log.New(log.Default().Writer(), "[UI] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

func Start(rootDir string) {
	htmlDir = path.Join(rootDir, "uisrv/html")

	mux := http.NewServeMux()
	mux.HandleFunc("/", renderRoot)
	mux.HandleFunc("/agent", renderAgent)
	mux.HandleFunc("/save_config", saveCustomConfigForInstance)
	mux.HandleFunc("/rotate_client_cert", rotateInstanceClientCert)
	mux.HandleFunc("/opamp_connection_settings", opampConnectionSettings)
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
	t, err := template.ParseFS(html.HtmlFS, "html/*")
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
	uid, err := uuid.Parse(r.URL.Query().Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	agent := data.AllAgents.GetAgentReadonlyClone(data.InstanceId(uid))
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

	uid, err := uuid.Parse(r.Form.Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	instanceId := data.InstanceId(uid)
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

	http.Redirect(w, r, "/agent?instanceid="+uid.String(), http.StatusSeeOther)
}

func rotateInstanceClientCert(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Find the agent instance.
	uid, err := uuid.Parse(r.Form.Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	instanceId := data.InstanceId(uid)
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Create a new certificate for the agent.
	certificate, err := certs.CreateTLSCert(certs.CaCert, certs.CaKey)
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

	http.Redirect(w, r, "/agent?instanceid="+uid.String(), http.StatusSeeOther)
}

func opampConnectionSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Find the agent instance.
	uid, err := uuid.Parse(r.Form.Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	instanceId := data.InstanceId(uid)
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// parse tls_min
	tlsMinVal := r.Form.Get("tls_min")
	var tlsMin string
	switch tlsMinVal {
	case "TLSv1.0":
		tlsMin = "1.0"
	case "TLSv1.1":
		tlsMin = "1.1"
	case "TLSv1.2":
		tlsMin = "1.2"
	case "TLSv1.3":
		tlsMin = "1.3"
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	offers := &protobufs.ConnectionSettingsOffers{
		Opamp: &protobufs.OpAMPConnectionSettings{
			Tls: &protobufs.TLSConnectionSettings{
				CaPemContents: string(certs.CaCert),
				MinVersion:    tlsMin,
				MaxVersion:    "1.3",
			},
		},
	}

	rawProxyURL := r.Form.Get("proxy_url")
	if len(rawProxyURL) > 0 {
		proxyURL, err := url.Parse(rawProxyURL)
		if err != nil {
			logger.Printf("Unable to parse %q as URL: %v", rawProxyURL, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		offers.Opamp.Proxy = &protobufs.ProxyConnectionSettings{
			Url: proxyURL.String(),
		}
	}

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

	http.Redirect(w, r, "/agent?instanceid="+uid.String(), http.StatusSeeOther)
}
