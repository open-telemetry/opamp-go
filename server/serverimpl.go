package server

import (
	"context"
	"errors"
	"io"
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
const headerContentType = "Content-Type"
const contentTypeProtobuf = "application/x-protobuf"

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
		Handler:     mux,
		Addr:        settings.ListenEndpoint,
		TLSConfig:   settings.TLSConfig,
		ConnContext: contextWithConn,
	}
	s.httpServer = hs

	listenAddr := s.httpServer.Addr

	// Start the HTTP Server in background.
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

	// HTTP connection is accepted. Check if it is a plain HTTP request.

	if req.Header.Get(headerContentType) == contentTypeProtobuf {
		// Yes, a plain HTTP request.
		s.handlePlainHTTPRequest(req, w)
		return
	}

	// No, it is a WebSocket. Upgrade it.
	conn, err := s.wsUpgrader.Upgrade(w, req, nil)
	if err != nil {
		s.logger.Errorf("Cannot upgrade HTTP connection to WebSocket: %v", err)
		return
	}

	// Return from this func to reduce memory usage.
	// Handle the connection on a separate goroutine.
	go s.handleWSConnection(conn)
}

func (s *server) handleWSConnection(wsConn *websocket.Conn) {
	agentConn := wsConnection{wsConn: wsConn}

	defer func() {
		// Close the connection when all is done.
		defer func() {
			err := wsConn.Close()
			if err != nil {
				s.logger.Errorf("error closing the WebSocket connection: %v", err)
			}
		}()

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
			response := s.settings.Callbacks.OnMessage(agentConn, &request)
			if response.InstanceUid == "" {
				response.InstanceUid = request.InstanceUid
			}
			err = agentConn.Send(context.Background(), response)
			if err != nil {
				s.logger.Errorf("Cannot send message to WebSocket: %v", err)
			}
		}
	}
}

func (s *server) handlePlainHTTPRequest(req *http.Request, w http.ResponseWriter) {
	bytes, err := io.ReadAll(req.Body)
	if err != nil {
		s.logger.Debugf("Cannot read HTTP body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Decode the message as a Protobuf message.
	var request protobufs.AgentToServer
	err = proto.Unmarshal(bytes, &request)
	if err != nil {
		s.logger.Debugf("Cannot decode message from HTTP Body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	agentConn := httpConnection{
		conn: connFromRequest(req),
	}

	if s.settings.Callbacks == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	s.settings.Callbacks.OnConnected(agentConn)

	defer func() {
		// Indicate via the callback that the OpAMP Connection is closed. From OpAMP
		// perspective the connection represented by this http request
		// is closed. It is not possible to send or receive more OpAMP messages
		// via this agentConn.
		s.settings.Callbacks.OnConnectionClose(agentConn)
	}()

	response := s.settings.Callbacks.OnMessage(agentConn, &request)

	// Set the InstanceUid if it is not set by the callback.
	if response.InstanceUid == "" {
		response.InstanceUid = request.InstanceUid
	}

	// Marshal the response.
	bytes, err = proto.Marshal(response)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Send the response.
	w.Header().Set(headerContentType, contentTypeProtobuf)
	_, err = w.Write(bytes)

	if err != nil {
		s.logger.Debugf("Cannot send HTTP response: %v", err)
	}
}
