package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/coder/websocket"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

const (
	defaultShutdownTimeout = 5 * time.Second
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
	// It is appended to with every redirect followed, and zeroed on a succesful
	// connection. responseChain should only be referred to by the goroutine that
	// runs tryConnectOnce and its synchronous callees.
	responseChain []*http.Response
}

// NewWebSocket creates a new OpAMP Client that uses WebSocket transport.
func NewWebSocket(logger types.Logger) *wsClient {
	if logger == nil {
		logger = &sharedinternal.NopLogger{}
	}

	sender := internal.NewSender(logger)
	w := &wsClient{
		common:              internal.NewClientCommon(logger, sender),
		sender:              sender,
		connShutdownTimeout: defaultShutdownTimeout,
	}
	return w
}

func (c *wsClient) Start(ctx context.Context, settings types.StartSettings) error {
	if err := c.common.PrepareStart(ctx, settings); err != nil {
		return err
	}

	// Build the transport (TLS + proxy settings are applied to it).
	c.transport = &http.Transport{
		TLSClientConfig:    settings.TLSConfig,
		ProxyConnectHeader: settings.ProxyHeaders,
	}

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

func (c *wsClient) Stop(ctx context.Context) error {
	// AgentDisconnect MUST be set in the last AgentToServer message sent from the Client to the Server.
	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.AgentDisconnect = &protobufs.AgentDisconnect{}
		},
	)
	c.sender.ScheduleSend()
	return c.common.Stop(ctx)
}

func (c *wsClient) AgentDescription() *protobufs.AgentDescription {
	return c.common.AgentDescription()
}

func (c *wsClient) SetAgentDescription(descr *protobufs.AgentDescription) error {
	return c.common.SetAgentDescription(descr)
}

func (c *wsClient) RequestConnectionSettings(request *protobufs.ConnectionSettingsRequest) error {
	return c.common.RequestConnectionSettings(request)
}

func (c *wsClient) SetHealth(health *protobufs.ComponentHealth) error {
	return c.common.SetHealth(health)
}

func (c *wsClient) UpdateEffectiveConfig(ctx context.Context) error {
	return c.common.UpdateEffectiveConfig(ctx)
}

func (c *wsClient) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	return c.common.SetRemoteConfigStatus(status)
}

// SetConnectionSettingsStatus sets the current ConnectionSettingsStatus and sends
// it to the Server. Must be called after processing connection settings offers to
// report APPLIED or FAILED status.
func (c *wsClient) SetConnectionSettingsStatus(status *protobufs.ConnectionSettingsStatus) error {
	return c.common.SetConnectionSettingsStatus(status)
}

func (c *wsClient) SetPackageStatuses(statuses *protobufs.PackageStatuses) error {
	return c.common.SetPackageStatuses(statuses)
}

func (c *wsClient) SetCustomCapabilities(customCapabilities *protobufs.CustomCapabilities) error {
	return c.common.SetCustomCapabilities(customCapabilities)
}

func (c *wsClient) SetFlags(flags protobufs.AgentToServerFlags) {
	c.common.SetFlags(flags)
}

func (c *wsClient) SendCustomMessage(message *protobufs.CustomMessage) (messageSendingChannel chan struct{}, err error) {
	return c.common.SendCustomMessage(message)
}

// SetAvailableComponents implements OpAMPClient.SetAvailableComponents
func (c *wsClient) SetAvailableComponents(components *protobufs.AvailableComponents) error {
	return c.common.SetAvailableComponents(components)
}

// SetCapabilities implements OpAMPClient.
func (c *wsClient) SetCapabilities(capabilities *protobufs.AgentCapabilities) error {
	return c.common.SetCapabilities(capabilities)
}

func viaReq(resps []*http.Response) []*http.Request {
	reqs := make([]*http.Request, 0, len(resps))
	for _, resp := range resps {
		reqs = append(reqs, resp.Request)
	}
	return reqs
}

// handleRedirect checks a failed websocket upgrade response for a 3xx response
// and a Location header. If found, it sets the URL to the location found in the
// header so that it is tried on the next retry, instead of the current URL.
func (c *wsClient) handleRedirect(ctx context.Context, resp *http.Response) error {
	// append to the responseChain so that subsequent redirects will have access
	c.responseChain = append(c.responseChain, resp)

	// very liberal handling of 3xx that largely ignores HTTP semantics
	redirect, err := resp.Location()
	if err != nil {
		c.common.Logger.Errorf(ctx, "%d redirect, but no valid location: %s", resp.StatusCode, err)
		return err
	}

	// It's slightly tricky to make CheckRedirect work. The WS HTTP request is
	// formed within the websocket library. To work around that, copy the
	// previous request, available in the response, and set the URL to the new
	// location. It should then result in the same URL that the websocket
	// library will form.
	nextRequest := resp.Request.Clone(ctx)
	nextRequest.URL = redirect

	// if CheckRedirect results in an error, it gets returned, terminating
	// redirection. As with stdlib, the error is wrapped in url.Error.
	if c.common.Callbacks.CheckRedirect != nil {
		if err := c.common.Callbacks.CheckRedirect(nextRequest, viaReq(c.responseChain), c.responseChain); err != nil {
			return &url.Error{
				Op:  "Get",
				URL: nextRequest.URL.String(),
				Err: err,
			}
		}
	}

	// rewrite the scheme for the sake of tolerance
	if redirect.Scheme == "http" {
		redirect.Scheme = "ws"
	} else if redirect.Scheme == "https" {
		redirect.Scheme = "wss"
	}
	c.common.Logger.Debugf(ctx, "%d redirect to %s", resp.StatusCode, redirect)

	// Set the URL to the redirect, so that it connects to it on the
	// next cycle.
	c.url = redirect

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
			c.responseChain = nil
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

	return sharedinternal.OptionalDuration{Defined: false}, nil
}

// Continuously try until connected. Will return nil when successfully
// connected. Will return error if it is cancelled via context.
func (c *wsClient) ensureConnected(ctx context.Context) error {
	infiniteBackoff := backoff.NewExponentialBackOff()

	// Make ticker run forever.
	infiniteBackoff.MaxElapsedTime = 0

	interval := time.Duration(0)

	for {
		timer := time.NewTimer(interval)
		interval = infiniteBackoff.NextBackOff()

		select {
		case <-timer.C:
			{
				if retryAfter, err := c.tryConnectOnce(ctx); err != nil {
					c.lastInternalErr.Store(&err)
					if errors.Is(err, context.Canceled) {
						c.common.Logger.Debugf(ctx, "Client is stopped, will not try anymore.")
						return err
					} else {
						c.common.Logger.Errorf(ctx, "Connection failed (%v), will retry.", err)
					}
					// Retry again a bit later.

					if retryAfter.Defined && retryAfter.Duration > interval {
						// If the Server suggested connecting later than our interval
						// then honour Server's request, otherwise wait at least
						// as much as we calculated.
						interval = retryAfter.Duration
					}

					continue
				}
				// Connected successfully.
				return nil
			}

		case <-ctx.Done():
			c.common.Logger.Debugf(ctx, "Client is stopped, will not try anymore.")
			timer.Stop()
			return ctx.Err()
		}
	}
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
		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			_ = c.conn.Close(websocket.StatusNormalClosure, "Normal closure")
		}()
		select {
		case <-closeDone:
			c.common.Logger.Debugf(ctx, "Close handshake completed.")
		case <-time.After(c.connShutdownTimeout):
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
		select {
		case <-r.IsStopped():
			c.common.Logger.Debugf(ctx, "Receiver stopped.")
		case <-time.After(c.connShutdownTimeout):
			c.common.Logger.Debugf(ctx, "Timeout waiting for receiver to stop.")
			<-r.IsStopped()
		}

	case <-r.IsStopped():
		// Receiver stopped — connection error or server closed. Start over.
		stopSender()
		<-c.sender.IsStopped()
	}
}

func (c *wsClient) runUntilStopped(ctx context.Context) {
	// Iterates until we detect that the client is stopping.
	sendFirstMessage := true
	for {
		if c.common.IsStopping() {
			return
		}

		c.runOneCycle(ctx, sendFirstMessage)
		sendFirstMessage = false
	}
}
