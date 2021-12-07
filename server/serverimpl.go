package server

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var (
	errAlreadyStarted = errors.New("already started")
)

const defaultOpAMPPath = "/v1/opamp"

type server struct {
	logger   types.Logger
	settings Settings

	// Upgrader to use to upgrade HTTP to WebSocket.
	wsUpgrader websocket.Upgrader

	// The listening HTTP Server after successful Start() call. Nil if Start()
	// is not called or was not successful.
	httpServer *http.Server
}

var _ OpAMPServer = (*server)(nil)

func New(logger types.Logger) *server {
	if logger == nil {
		logger = &internal.NopLogger{}
	}

	return &server{logger: logger}
}

func (s *server) Attach(settings Settings) (HTTPHandlerFunc, error) {
	s.settings = settings
	s.wsUpgrader = websocket.Upgrader{}
	return s.httpHandler, nil
}

func (s *server) Start(settings StartSettings) error {
	if s.httpServer != nil {
		return errAlreadyStarted
	}

	_, err := s.Attach(settings.Settings)
	if err != nil {
		return err
	}

	// Prepare handling OpAMP incoming HTTP requests on the requests URL path.
	mux := http.NewServeMux()

	path := settings.ListenPath
	if path == "" {
		path = defaultOpAMPPath
	}

	mux.HandleFunc(path, s.httpHandler)

	hs := &http.Server{
		Handler:   mux,
		Addr:      settings.ListenEndpoint,
		TLSConfig: settings.TLSConfig,
	}
	s.httpServer = hs

	listenAddr := s.httpServer.Addr

	// Start the HTTP server in background.
	if hs.TLSConfig != nil {
		if listenAddr == "" {
			listenAddr = ":https"
		}
		err = s.startHttpServer(
			listenAddr,
			func(l net.Listener) error { return hs.ServeTLS(l, "", "") },
		)
	} else {
		if listenAddr == "" {
			listenAddr = ":http"
		}
		err = s.startHttpServer(
			listenAddr,
			func(l net.Listener) error { return hs.Serve(l) },
		)
	}
	return err
}

func (s *server) startHttpServer(listenAddr string, serveFunc func(l net.Listener) error) error {
	// If the listen address is not specified use the default.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}

	// Begin serving connections in the background.
	go func() {
		err = serveFunc(ln)

		// ErrServerClosed is expected after successful Stop(), so we won't log that
		// particular error.
		if err != nil && err != http.ErrServerClosed {
			s.logger.Errorf("Error running HTTP Server: %v", err)
		}
	}()

	return nil
}

func (s *server) Stop(ctx context.Context) error {
	if s.httpServer != nil {
		defer func() { s.httpServer = nil }()
		// This stops accepting new connections. TODO: close existing
		// connections and wait them to be terminated.
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

func (s *server) httpHandler(w http.ResponseWriter, req *http.Request) {
	if s.settings.Callbacks != nil {
		resp := s.settings.Callbacks.OnConnecting(req)
		if !resp.Accept {
			// HTTP connection is not accepted. Set the response headers.
			for k, v := range resp.HTTPResponseHeader {
				w.Header().Set(k, v)
			}
			// And write the response status code.
			w.WriteHeader(resp.HTTPStatusCode)
			return
		}
	}

	// HTTP connection is accepted. Upgrade it to WebSocket.
	conn, err := s.wsUpgrader.Upgrade(w, req, nil)
	if err != nil {
		s.logger.Errorf("Cannot upgrade HTTP connection to WebSocket: %v", err)
		return
	}

	// Return from this func to reduce memory usage.
	// Handle the connection on a separate gorountine.
	go s.handleWSConnection(conn)
}

func (s *server) handleWSConnection(wsConn *websocket.Conn) {
	agentConn := connection{wsConn: wsConn}

	defer func() {
		// Close the connection when all is done.
		defer wsConn.Close()

		if s.settings.Callbacks != nil {
			s.settings.Callbacks.OnConnectionClose(agentConn)
		}
	}()

	if s.settings.Callbacks != nil {
		s.settings.Callbacks.OnConnected(agentConn)
	}

	// Loop until fail to read from the WebSocket connection.
	for {
		// Block until the next message can be read.
		mt, bytes, err := wsConn.ReadMessage()
		if err != nil {
			if !websocket.IsUnexpectedCloseError(err) {
				s.logger.Errorf("Cannot read a message from WebSocket: %v", err)
				break
			}
			// This is a normal closing of the WebSocket connection.
			s.logger.Debugf("Agent disconnected: %v", err)
			break
		}
		if mt != websocket.BinaryMessage {
			s.logger.Errorf("Received unexpected message type from WebSocket: %v", mt)
			continue
		}

		// Decode WebSocket message as a Protobuf message.
		var request protobufs.AgentToServer
		err = proto.Unmarshal(bytes, &request)
		if err != nil {
			s.logger.Errorf("Cannot decode message from WebSocket: %v", err)
			continue
		}

		if s.settings.Callbacks != nil {
			s.settings.Callbacks.OnMessage(agentConn, &request)
		}
	}
}
