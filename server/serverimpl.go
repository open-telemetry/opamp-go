package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	serverTypes "github.com/open-telemetry/opamp-go/server/types"
)

var (
	errAlreadyStarted = errors.New("already started")
)

const defaultOpAMPPath = "/v1/opamp"
const headerContentType = "Content-Type"
const headerContentEncoding = "Content-Encoding"
const headerAcceptEncoding = "Accept-Encoding"
const contentEncodingGzip = "gzip"
const contentTypeProtobuf = "application/x-protobuf"

type server struct {
	logger   types.Logger
	settings Settings

	// Upgrader to use to upgrade HTTP to WebSocket.
	wsUpgrader websocket.Upgrader

	// The listening HTTP Server after successful Start() call. Nil if Start()
	// is not called or was not successful.
	httpServer *http.Server

	// The network address Server is listening on. Nil if not started.
	addr net.Addr
}

var _ OpAMPServer = (*server)(nil)

// New creates a new OpAMP Server.
func New(logger types.Logger) *server {
	if logger == nil {
		logger = &internal.NopLogger{}
	}

	return &server{logger: logger}
}

func (s *server) Attach(settings Settings) (HTTPHandlerFunc, ConnContext, error) {
	s.settings = settings
	s.wsUpgrader = websocket.Upgrader{
		EnableCompression: settings.EnableCompression,
	}
	return s.httpHandler, contextWithConn, nil
}

func (s *server) Start(settings StartSettings) error {
	if s.httpServer != nil {
		return errAlreadyStarted
	}

	_, _, err := s.Attach(settings.Settings)
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
	s.addr = ln.Addr()

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

func (s *server) Addr() net.Addr {
	return s.addr
}

func (s *server) httpHandler(w http.ResponseWriter, req *http.Request) {
	var connectionCallbacks serverTypes.ConnectionCallbacks
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
		// use connection-specific handler provided by ConnectionResponse
		connectionCallbacks = resp.ConnectionCallbacks
	}

	// HTTP connection is accepted. Check if it is a plain HTTP request.

	if req.Header.Get(headerContentType) == contentTypeProtobuf {
		// Yes, a plain HTTP request.
		s.handlePlainHTTPRequest(req, w, connectionCallbacks)
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
	go s.handleWSConnection(conn, connectionCallbacks)
}

func (s *server) handleWSConnection(wsConn *websocket.Conn, connectionCallbacks serverTypes.ConnectionCallbacks) {
	agentConn := wsConnection{wsConn: wsConn, connMutex: &sync.Mutex{}}

	defer func() {
		// Close the connection when all is done.
		defer func() {
			err := wsConn.Close()
			if err != nil {
				s.logger.Errorf("error closing the WebSocket connection: %v", err)
			}
		}()

		if connectionCallbacks != nil {
			connectionCallbacks.OnConnectionClose(agentConn)
		}
	}()

	if connectionCallbacks != nil {
		connectionCallbacks.OnConnected(agentConn)
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
		err = internal.DecodeWSMessage(bytes, &request)
		if err != nil {
			s.logger.Errorf("Cannot decode message from WebSocket: %v", err)
			continue
		}

		if connectionCallbacks != nil {
			response := connectionCallbacks.OnMessage(agentConn, &request)
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

func decompressGzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (s *server) readReqBody(req *http.Request) ([]byte, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if req.Header.Get(headerContentEncoding) == contentEncodingGzip {
		data, err = decompressGzip(data)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

func compressGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write(data)
	if err != nil {
		return nil, err
	}
	err = w.Close()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *server) handlePlainHTTPRequest(req *http.Request, w http.ResponseWriter, connectionCallbacks serverTypes.ConnectionCallbacks) {
	bytes, err := s.readReqBody(req)
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

	if connectionCallbacks == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	connectionCallbacks.OnConnected(agentConn)

	defer func() {
		// Indicate via the callback that the OpAMP Connection is closed. From OpAMP
		// perspective the connection represented by this http request
		// is closed. It is not possible to send or receive more OpAMP messages
		// via this agentConn.
		connectionCallbacks.OnConnectionClose(agentConn)
	}()

	response := connectionCallbacks.OnMessage(agentConn, &request)

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
	if req.Header.Get(headerAcceptEncoding) == contentEncodingGzip {
		bytes, err = compressGzip(bytes)
		if err != nil {
			s.logger.Errorf("Cannot compress response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set(headerContentEncoding, contentEncodingGzip)
	}
	_, err = w.Write(bytes)

	if err != nil {
		s.logger.Debugf("Cannot send HTTP response: %v", err)
	}
}
