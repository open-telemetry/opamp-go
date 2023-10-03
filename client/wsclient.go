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
	"github.com/gorilla/websocket"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var errStopping = errors.New("client is stopping or stopped, no more messages can be sent")
var errEarlyStop = errors.New("context canceled before shutdown could complete")

// wsClient is an OpAMP Client implementation for WebSocket transport.
// See specification: https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#websocket-transport
type wsClient struct {
	common internal.ClientCommon

	// OpAMP Server URL.
	url *url.URL

	// HTTP request headers to use when connecting to OpAMP Server.
	requestHeader http.Header

	// Websocket dialer and connection.
	dialer    websocket.Dialer
	conn      *websocket.Conn
	connMutex sync.RWMutex

	// The sender is responsible for sending portion of the OpAMP protocol.
	sender *internal.WSSender

	isStopped atomic.Bool

	// Sends a signal to stop background processors that asynchronously use the
	// WebSocket connection, e.g. WSSender.
	stopBGProcessors chan struct{}

	// Responds to a signal from stopProcessors indicating that all processors
	// have been stopped.
	bgProcessorsStopped chan struct{}

	// Network connection timeout used for the WebSocket closing handshake.
	// This field is currently only modified during testing.
	connShutdownTimeout time.Duration
}

var _ OpAMPClient = &wsClient{}

// NewWebSocket creates a new OpAMP Client that uses WebSocket transport.
func NewWebSocket(logger types.Logger) *wsClient {
	if logger == nil {
		logger = &sharedinternal.NopLogger{}
	}

	sender := internal.NewSender(logger)
	w := &wsClient{
		common:              internal.NewClientCommon(logger, sender),
		sender:              sender,
		stopBGProcessors:    make(chan struct{}, 1),
		bgProcessorsStopped: make(chan struct{}, 1),
		connShutdownTimeout: 10 * time.Second,
	}
	return w
}

func (c *wsClient) Start(ctx context.Context, settings types.StartSettings) error {
	if err := c.common.PrepareStart(ctx, settings); err != nil {
		return err
	}

	// Prepare connection settings.
	c.dialer = *websocket.DefaultDialer

	var err error
	c.url, err = url.Parse(settings.OpAMPServerURL)
	if err != nil {
		return err
	}

	c.dialer.EnableCompression = settings.EnableCompression

	if settings.TLSConfig != nil {
		c.url.Scheme = "wss"
	}
	c.dialer.TLSClientConfig = settings.TLSConfig

	c.requestHeader = settings.Header

	c.common.StartConnectAndRun(c.runUntilStopped)

	return nil
}

func (c *wsClient) Stop(ctx context.Context) error {
	c.isStopped.Store(true)

	// Close connection if any.
	c.connMutex.RLock()
	conn := c.conn
	c.connMutex.RUnlock()

	if conn != nil {
		// Shut down the sender and any other background processors.
		c.stopBGProcessors <- struct{}{}
		select {
		case <-c.bgProcessorsStopped:
		case <-ctx.Done():
			_ = c.conn.Close()
			return errEarlyStop
		}

		defaultCloseHandler := conn.CloseHandler()
		closed := make(chan struct{})

		// The server should respond with a close message of its own, which will
		// trigger this callback. At this point the close sequence has been
		// completed and the TCP connection can be gracefully closed.
		conn.SetCloseHandler(func(code int, text string) error {
			err := defaultCloseHandler(code, text)
			closed <- struct{}{}
			return err
		})

		message := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
		err := conn.WriteControl(websocket.CloseMessage, message, time.Now().Add(c.connShutdownTimeout))

		if err != nil {
			_ = c.conn.Close()
			return fmt.Errorf("could not write close message to WebSocket, connection closed without performing closing handshake: %w", err)
		}

		select {
		case <-closed:
			// runOneCycle will close the connection if the closing handshake completed,
			// so there's no need to close it here.
		case <-time.After(c.connShutdownTimeout):
			_ = c.conn.Close()
		case <-ctx.Done():
			_ = c.conn.Close()
			return errEarlyStop
		}
	}

	return c.common.Stop(ctx)
}

func (c *wsClient) AgentDescription() *protobufs.AgentDescription {
	return c.common.AgentDescription()
}

func (c *wsClient) SetAgentDescription(descr *protobufs.AgentDescription) error {
	if c.isStopped.Load() {
		return errStopping
	}
	return c.common.SetAgentDescription(descr)
}

func (c *wsClient) SetHealth(health *protobufs.AgentHealth) error {
	if c.isStopped.Load() {
		return errStopping
	}
	return c.common.SetHealth(health)
}

func (c *wsClient) UpdateEffectiveConfig(ctx context.Context) error {
	if c.isStopped.Load() {
		return errStopping
	}
	return c.common.UpdateEffectiveConfig(ctx)
}

func (c *wsClient) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	if c.isStopped.Load() {
		return errStopping
	}
	return c.common.SetRemoteConfigStatus(status)
}

func (c *wsClient) SetPackageStatuses(statuses *protobufs.PackageStatuses) error {
	if c.isStopped.Load() {
		return errStopping
	}
	return c.common.SetPackageStatuses(statuses)
}

// Try to connect once. Returns an error if connection fails and optional retryAfter
// duration to indicate to the caller to retry after the specified time as instructed
// by the Server.
func (c *wsClient) tryConnectOnce(ctx context.Context) (err error, retryAfter sharedinternal.OptionalDuration) {
	var resp *http.Response
	conn, resp, err := c.dialer.DialContext(ctx, c.url.String(), c.requestHeader)
	if err != nil {
		if c.common.Callbacks != nil && !c.common.IsStopping() {
			c.common.Callbacks.OnConnectFailed(err)
		}
		if resp != nil {
			c.common.Logger.Errorf("Server responded with status=%v", resp.Status)
			duration := sharedinternal.ExtractRetryAfterHeader(resp)
			return err, duration
		}
		return err, sharedinternal.OptionalDuration{Defined: false}
	}

	// Successfully connected.
	c.connMutex.Lock()
	c.conn = conn
	c.connMutex.Unlock()
	if c.common.Callbacks != nil {
		c.common.Callbacks.OnConnect()
	}

	return nil, sharedinternal.OptionalDuration{Defined: false}
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
				if err, retryAfter := c.tryConnectOnce(ctx); err != nil {
					if errors.Is(err, context.Canceled) {
						c.common.Logger.Debugf("Client is stopped, will not try anymore.")
						return err
					} else {
						c.common.Logger.Errorf("Connection failed (%v), will retry.", err)
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
			c.common.Logger.Debugf("Client is stopped, will not try anymore.")
			timer.Stop()
			return ctx.Err()
		}
	}
}

// runOneCycle performs the following actions:
//  1. connect (try until succeeds).
//  2. set up a background processor to send messages.
//  3. send first status report.
//  4. receive and process messages until an error occurs or the connection closes.
//
// If it encounters an error it closes the connection and returns.
// Will stop and return if Stop() is called.
func (c *wsClient) runOneCycle(ctx context.Context) {
	if err := c.ensureConnected(ctx); err != nil {
		// Can't connect, so can't move forward. This currently happens when we
		// are being stopped.
		return
	}

	defer func() {
		// Close the connection.
		_ = c.conn.Close()

		// Unset the closed connection to indicate that it is closed.
		c.connMutex.Lock()
		c.conn = nil
		c.connMutex.Unlock()
	}()

	if c.common.IsStopping() {
		return
	}

	// Prepare the first status report.
	err := c.common.PrepareFirstMessage(ctx)
	if err != nil {
		c.common.Logger.Errorf("cannot prepare the first message:%v", err)
		return
	}

	// Create a cancellable context for background processors.
	procCtx, procCancel := context.WithCancel(ctx)

	// Stop background processors if we receive a signal to do so.
	// Note that the receiver does not respond to signals and
	// will only stop when the connection closes or errors.
	go func() {
		select {
		case <-c.stopBGProcessors:
			procCancel()
			c.sender.WaitToStop()
			c.bgProcessorsStopped <- struct{}{}
		case <-procCtx.Done():
		}
	}()

	// Connected successfully. Start the sender. This will also send the first
	// status report.
	if err := c.sender.Start(procCtx, c.conn); err != nil {
		c.common.Logger.Errorf("Failed to send first status report: %v", err)
		// We could not send the report, the only thing we can do is start over.
		procCancel()
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
		c.common.Capabilities,
	)
	r.ReceiverLoop(ctx)

	// If we exited receiverLoop it means there is a connection error or the closing handshake
	// has completed. We cannot read messages anymore, so clean up the connection.
	// If there is a connection error we will need to start over.

	// Stop the background processors.
	procCancel()

	// Wait for WSSender to stop.
	c.sender.WaitToStop()
}

func (c *wsClient) runUntilStopped(ctx context.Context) {
	// Iterates until we detect that the client is stopping.
	for {
		if c.common.IsStopping() || c.isStopped.Load() {
			return
		}
		c.runOneCycle(ctx)
	}
}
