package server

import (
	"context"
	"crypto/tls"
	"net/http"

	"github.com/open-telemetry/opamp-go/server/types"
)

type Settings struct {
	// Callbacks that the server will call after successful Attach/Start.
	Callbacks types.Callbacks
}

type StartSettings struct {
	Settings

	// ListenEndpoint specifies the endpoint to listen on, e.g. "127.0.0.1:4320"
	ListenEndpoint string

	// ListenPath specifies the URL path on which to accept the OpAMP connections
	// If this is empty string then Start() will use the default "/v1/opamp" path.
	ListenPath string

	// Server's TLS configuration.
	TLSConfig *tls.Config
}

type HTTPHandlerFunc func(http.ResponseWriter, *http.Request)

type OpAMPServer interface {
	// Attach prepares the OpAMP Server to begin handling requests from an existing
	// http.Server. The returned HTTPHandlerFunc should be added as a handler to the
	// desired http.Server by the caller and the http.Server should be started by
	// the caller after that.
	// For example:
	//   handler, _ := server.Attach()
	//   mux := http.NewServeMux()
	//   mux.HandleFunc("/opamp", handler)
	//   httpSrv := &http.Server{Handler:mux,Addr:"127.0.0.1:4320"}
	//   httpSrv.ListenAndServe()
	Attach(settings Settings) (HTTPHandlerFunc, error)

	// Start an OpAMP Server and begin accepting connections. Starts its own http.Server
	// using provided settings. This should block until the http.Server is ready to
	// accept connections.
	Start(settings StartSettings) error

	// Stop accepting new connections and close all current connections. This should
	// block until all connections are closed.
	Stop(ctx context.Context) error
}
