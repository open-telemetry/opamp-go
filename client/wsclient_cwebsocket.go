//go:build cwebsocket

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var _ OpAMPClient = (*wsClient)(nil)

// wsClient is an OpAMP Client implementation for WebSocket transport.
// See specification: https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#websocket-transport
type wsClient struct {
	common internal.ClientCommon

	// OpAMP Server URL.
	url *url.URL

	// HTTP request headers to use when connecting to OpAMP Server.
	getHeader func() http.Header

	// transport is the underlying HTTP transport used for dialing.
	// dialOpts references it via dialOpts.HTTPClient.Transport.
	transport *http.Transport
	// dialOpts holds options for each websocket.Dial call.
	dialOpts *websocket.DialOptions

	// Websocket connection.
	conn      *websocket.Conn
	connMutex sync.RWMutex

	// The sender is responsible for sending portion of the OpAMP protocol.
	sender *internal.WSSender

	// last non-nil internal error that was encountered in the conn retry loop,
	// currently used only for testing.
	lastInternalErr atomic.Pointer[error]

	// Network connection timeout used for the WebSocket closing handshake.
	// This field is currently only modified during testing.
	connShutdownTimeout time.Duration

	// responseChain is used for the "via" argument in CheckRedirect.
	// It is appended to with every redirect followed, and zeroed on a successful
	// connection. responseChain should only be referred to by the goroutine that
	// runs tryConnectOnce and its synchronous callees.
	responseChain []*http.Response
}

func (c *wsClient) Start(ctx context.Context, settings types.StartSettings) error {
	if err := c.common.PrepareStart(ctx, settings); err != nil {
		return err
	}

	// Clone the default transport to inherit timeouts, connection pooling,
	// HTTP/2 settings, and ProxyFromEnvironment.
	c.transport = http.DefaultTransport.(*http.Transport).Clone()
	c.transport.TLSClientConfig = settings.TLSConfig
	c.transport.ProxyConnectHeader = settings.ProxyHeaders

	if settings.ProxyURL != "" {
		proxyURL, err := url.Parse(settings.ProxyURL)
		if err != nil {
			return fmt.Errorf("unable to parse proxy url setting %q: %w", settings.ProxyURL, err)
		}
		c.transport.Proxy = http.ProxyURL(proxyURL)
	}

	// The HTTP client must not follow redirects itself; we handle them manually
	// so that we can invoke the CheckRedirect callback with the right types.
	httpClient := &http.Client{
		Transport: c.transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Compression mode. CompressionContextTakeover matches gorilla/websocket's
	// EnableCompression behaviour (context takeover, efficient for repeated
	// content). If the server only supports no-context-takeover, coder falls
	// back automatically.
	compressionMode := websocket.CompressionDisabled
	if settings.EnableCompression {
		compressionMode = websocket.CompressionContextTakeover
	}

	c.dialOpts = &websocket.DialOptions{
		HTTPClient:      httpClient,
		CompressionMode: compressionMode,
	}

	var err error
	c.url, err = url.Parse(settings.OpAMPServerURL)
	if err != nil {
		return err
	}

	if settings.TLSConfig != nil {
		c.url.Scheme = "wss"
	}

	headerFunc := settings.HeaderFunc
	if headerFunc == nil {
		headerFunc = func(h http.Header) http.Header {
			return h
		}
	}

	baseHeader := settings.Header
	if baseHeader == nil {
		baseHeader = http.Header{}
	}

	c.getHeader = func() http.Header {
		return headerFunc(baseHeader.Clone())
	}

	c.common.StartConnectAndRun(c.runUntilStopped)

	return nil
}

// Try to connect once. Returns an error if connection fails and optional retryAfter
// duration to indicate to the caller to retry after the specified time as instructed
// by the Server.
func (c *wsClient) tryConnectOnce(ctx context.Context) (retryAfter sharedinternal.OptionalDuration, err error) {
	var resp *http.Response
	var redirecting bool
	defer func() {
		if err != nil && !redirecting {
			c.closeResponseChain()
			if !c.common.IsStopping() {
				c.common.Callbacks.OnConnectFailed(ctx, err)
			}
		}
	}()

	// Refresh headers on every attempt so HeaderFunc changes take effect.
	c.dialOpts.HTTPHeader = c.getHeader()

	conn, resp, err := websocket.Dial(ctx, c.url.String(), c.dialOpts)
	if err != nil {
		if resp != nil {
			duration := sharedinternal.ExtractRetryAfterHeader(resp)
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				redirecting = true
				if redirectErr := c.handleRedirect(ctx, resp); redirectErr != nil {
					return duration, redirectErr
				}
			} else {
				c.common.Logger.Errorf(ctx, "Server responded with status=%v", resp.Status)
			}
			return duration, err
		}
		return sharedinternal.OptionalDuration{Defined: false}, err
	}

	// Disable coder's default 32 KB read limit; the OpAMP spec does not impose one.
	conn.SetReadLimit(-1)

	// Successfully connected.
	c.connMutex.Lock()
	c.conn = conn
	c.connMutex.Unlock()
	c.common.Callbacks.OnConnect(ctx)
	c.closeResponseChain()

	return sharedinternal.OptionalDuration{Defined: false}, nil
}

// runOneCycle performs the following actions:
//  1. connect (try until succeeds).
//  2. send first status report or the next message if sendFirstMessage is false.
//  3. start the sender to wait for scheduled messages and send them to the server.
//  4. start the receiver to receive and process messages until an error happens.
//  5. wait until both the sender and receiver are stopped.
//
// runOneCycle will close the connection it created before it returns.
//
// When Stop() is called (ctx is cancelled, isStopping is set), wsClient shuts down gracefully:
//  1. The sender context is cancelled; the sender flushes any pending message
//     (including AgentDisconnect) and signals IsStopped.
//  2. runOneCycle stops the receiver, then performs the WebSocket close handshake
//     via conn.Close, which sends a close frame and waits for the server's close frame.
//  3. conn.CloseNow (deferred) ensures the socket is released in all paths.
func (c *wsClient) runOneCycle(ctx context.Context, sendFirstMessage bool) {
	if err := c.ensureConnected(ctx); err != nil {
		// Can't connect, so can't move forward. This currently happens when we
		// are being stopped.
		return
	}
	// Safety-net: always release the socket when runOneCycle returns.
	defer c.conn.CloseNow()

	if c.common.IsStopping() {
		return
	}

	if sendFirstMessage {
		// Prepare the first status report with full agent state.
		err := c.common.PrepareFirstMessage(ctx)
		if err != nil {
			c.common.Logger.Errorf(ctx, "cannot prepare the first message:%v", err)
			return
		}
	} else {
		// Send the next message even if it is empty
		c.sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {})
	}

	// Create a cancellable context for background processors.
	senderCtx, stopSender := context.WithCancel(ctx)
	defer stopSender()

	// Connected successfully. Start the sender. This will also send the first
	// message.
	if err := c.sender.Start(senderCtx, c.conn); err != nil {
		c.common.Logger.Errorf(senderCtx, "Failed to send message after connection: %v", err)
		// We could not send the report, the only thing we can do is start over.
		return
	}

	// First status report sent. Now loop to receive and process messages.
	r := internal.NewWSReceiver(
		c.common.Logger,
		c.common.Callbacks,
		c.conn,
		c.sender,
		&c.common.ClientSyncedState,
		c.common.PackagesStateProvider,
		&c.common.PackageSyncMutex,
		c.common.DownloadReporterInterval,
	)

	// The receiver runs until it sees a close or an error.
	receiverCtx, stopReceiver := context.WithCancel(context.Background())
	defer stopReceiver()
	r.Start(receiverCtx)

	select {
	case <-c.sender.IsStopped():
		// Sender stopped (either because ctx was cancelled for a graceful shutdown,
		// or because of an unrecoverable write error).
		if err := c.sender.StoppingErr(); err != nil {
			c.common.Logger.Debugf(ctx, "Error stopping the sender: %v", err)
			stopReceiver()
			<-r.IsStopped()
			break
		}

		// Clean sender stop — perform the WebSocket close handshake.
		//
		// IMPORTANT: We must call conn.Close BEFORE stopping the receiver. In
		// coder/websocket, cancelling the context passed to Read triggers
		// context.AfterFunc which calls c.close(), permanently closing the
		// underlying connection. conn.Close would then be unable to send the
		// close frame.
		//
		// Instead, we start conn.Close while the receiver is still running.
		// conn.Close sends a close frame then waits for the read mutex (held by
		// the receiver). When the server responds with a close ack, the
		// receiver's Read returns a CloseError and releases the mutex;
		// conn.Close acquires it, sees the close was received, and returns.
		//
		// If the server does not respond within connShutdownTimeout, we call
		// stopReceiver(), which cancels receiverCtx. Coder's context.AfterFunc
		// then calls c.close(), closing the underlying TCP connection. This
		// unblocks conn.Close's mutex wait and allows it to return quickly.
		shutdownTimer := time.NewTimer(c.connShutdownTimeout)
		defer shutdownTimer.Stop()

		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			_ = c.conn.Close(websocket.StatusNormalClosure, "Normal closure")
		}()
		select {
		case <-closeDone:
			c.common.Logger.Debugf(ctx, "Close handshake completed.")
		case <-shutdownTimer.C:
			c.common.Logger.Debugf(ctx, "Timeout waiting for close handshake, forcing close.")
			// Cancel the receiver's context; coder's setupReadTimeout AfterFunc
			// calls c.close() which closes the underlying conn and unblocks
			// conn.Close's internal read-lock wait.
			stopReceiver()
			<-closeDone
		}

		// Stop the receiver if it hasn't already stopped (it will have stopped
		// if the server's close ack triggered a CloseError, or after a timeout).
		c.common.Logger.Debugf(ctx, "Waiting for receiver to stop.")
		stopReceiver() // idempotent if already called above

		// Re-arm the timer for the receiver-stop wait.
		if !shutdownTimer.Stop() {
			select {
			case <-shutdownTimer.C:
			default:
			}
		}
		shutdownTimer.Reset(c.connShutdownTimeout)

		select {
		case <-r.IsStopped():
			c.common.Logger.Debugf(ctx, "Receiver stopped.")
		case <-shutdownTimer.C:
			// Receiver did not stop within the timeout; defer conn.CloseNow() will
			// close the underlying connection and unblock any in-flight Read call.
			c.common.Logger.Debugf(ctx, "Timeout waiting for receiver to stop.")
		}

	case <-r.IsStopped():
		// Receiver stopped — connection error or server closed. Start over.
		stopSender()
		<-c.sender.IsStopped()
	}
}
