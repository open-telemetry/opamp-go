package client

import (
	"context"
	"errors"
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

const (
	defaultShutdownTimeout = 5 * time.Second
)

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

	// last non-nil internal error that was encountered in the conn retry loop,
	// currently used only for testing.
	lastInternalErr atomic.Pointer[error]

	// Network connection timeout used for the WebSocket closing handshake.
	// This field is currently only modified during testing.
	connShutdownTimeout time.Duration
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

func (c *wsClient) SetPackageStatuses(statuses *protobufs.PackageStatuses) error {
	return c.common.SetPackageStatuses(statuses)
}

// Try to connect once. Returns an error if connection fails and optional retryAfter
// duration to indicate to the caller to retry after the specified time as instructed
// by the Server.
func (c *wsClient) tryConnectOnce(ctx context.Context) (retryAfter sharedinternal.OptionalDuration, err error) {
	var resp *http.Response
	conn, resp, err := c.dialer.DialContext(ctx, c.url.String(), c.requestHeader)
	if err != nil {
		if c.common.Callbacks != nil && !c.common.IsStopping() {
			c.common.Callbacks.OnConnectFailed(ctx, err)
		}
		if resp != nil {
			duration := sharedinternal.ExtractRetryAfterHeader(resp)
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				// very liberal handling of 3xx that largely ignores HTTP semantics
				redirect, err := resp.Location()
				if err != nil {
					c.common.Logger.Errorf(ctx, "%d redirect, but no valid location: %s", resp.StatusCode, err)
					return duration, err
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
			} else {
				c.common.Logger.Errorf(ctx, "Server responded with status=%v", resp.Status)
			}
			return duration, err
		}
		return sharedinternal.OptionalDuration{Defined: false}, err
	}

	// Successfully connected.
	c.connMutex.Lock()
	c.conn = conn
	c.connMutex.Unlock()
	if c.common.Callbacks != nil {
		c.common.Callbacks.OnConnect(ctx)
	}

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
//  2. send first status report.
//  3. start the sender to wait for scheduled message and send it to the server.
//  4. start the receiver to receive and process messages until error happens.
//  5. wait until both sender and receiver are stopped.
//
// runOneCycle will close the connection it created before it return.
//
// When Stop() is called (ctx is cancelled, isStopping is set), wsClient will shutdown gracefully:
//  1. sender will be cancelled by the ctx, send the close message to server and return the error via sender.Err().
//  2. runOneCycle will handle that error and wait for the close message from server until timeout.
func (c *wsClient) runOneCycle(ctx context.Context) {
	if err := c.ensureConnected(ctx); err != nil {
		// Can't connect, so can't move forward. This currently happens when we
		// are being stopped.
		return
	}
	// Close the underlying connection.
	defer c.conn.Close()

	if c.common.IsStopping() {
		return
	}

	// Prepare the first status report.
	err := c.common.PrepareFirstMessage(ctx)
	if err != nil {
		c.common.Logger.Errorf(ctx, "cannot prepare the first message:%v", err)
		return
	}

	// Create a cancellable context for background processors.
	senderCtx, stopSender := context.WithCancel(ctx)
	defer stopSender()

	// Connected successfully. Start the sender. This will also send the first
	// status report.
	if err := c.sender.Start(senderCtx, c.conn); err != nil {
		c.common.Logger.Errorf(senderCtx, "Failed to send first status report: %v", err)
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
		c.common.Capabilities,
	)
	r.Start(ctx)

	select {
	case <-c.sender.IsStopped():
		// sender will send close message to initiate the close handshake
		if err := c.sender.Err(); err != nil && err != websocket.ErrCloseSent {
			c.common.Logger.Debugf("failed to send close message, close without the handshake.")
			break
		}

		c.common.Logger.Debugf("waiting for close message from server.")
		select {
		case <-r.IsStopped():
			c.common.Logger.Debugf("shutdown handshake complete.")
		case <-time.After(c.connShutdownTimeout):
			c.common.Logger.Debugf("timeout waiting for close message.")
			// not receive close message from the server, close the connection to force the receive loop to stop
			_ = c.conn.Close()
			<-r.IsStopped()
		}
	case <-r.IsStopped():
		// If we exited receiverLoop it means there is a connection error, we cannot
		// read messages anymore. We need to start over.

		stopSender()
		<-c.sender.IsStopped()
	}
}

func (c *wsClient) runUntilStopped(ctx context.Context) {
	// Iterates until we detect that the client is stopping.
	for {
		if c.common.IsStopping() {
			return
		}

		c.runOneCycle(ctx)
	}
}
