//go:build !cwebsocket

package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	dialer "github.com/elastic/proxy-connect-dialer-go"

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

	// Prepare connection settings.
	c.dialer = *websocket.DefaultDialer

	if settings.ProxyURL != "" {
		if err := c.useProxy(settings.ProxyURL, settings.ProxyHeaders, settings.TLSConfig); err != nil {
			return err
		}
	}

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
	conn, resp, err := c.dialer.DialContext(ctx, c.url.String(), c.getHeader())
	if err != nil {
		if resp != nil {
			duration := sharedinternal.ExtractRetryAfterHeader(resp)
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				redirecting = true
				if err := c.handleRedirect(ctx, resp); err != nil {
					return duration, err
				}
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
// runOneCycle will close the connection it created before it return.
//
// When Stop() is called (ctx is cancelled, isStopping is set), wsClient will shutdown gracefully:
//  1. sender will be cancelled by the ctx, send the close message to server and return the error via sender.Err().
//  2. runOneCycle will handle that error and wait for the close message from server until timeout.
func (c *wsClient) runOneCycle(ctx context.Context, sendFirstMessage bool) {
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

	// When the wsclient is closed, the context passed to runOneCycle will be canceled.
	// The receiver should keep running and processing messages
	// until it received a Close message from the server which means the server has no more messages.
	receiverCtx, stopReceiver := context.WithCancel(context.Background())
	defer stopReceiver()
	r.Start(receiverCtx)

	select {
	case <-c.sender.IsStopped():
		// sender will send close message to initiate the close handshake
		if err := c.sender.StoppingErr(); err != nil {
			c.common.Logger.Debugf(ctx, "Error stopping the sender: %v", err)

			stopReceiver()
			<-r.IsStopped()
			break
		}

		c.common.Logger.Debugf(ctx, "Waiting for receiver to stop.")
		shutdownTimer := time.NewTimer(c.connShutdownTimeout)
		defer shutdownTimer.Stop()
		select {
		case <-r.IsStopped():
			c.common.Logger.Debugf(ctx, "Receiver stopped.")
		case <-shutdownTimer.C:
			c.common.Logger.Debugf(ctx, "Timeout waiting for receiver to stop.")
			stopReceiver()
			<-r.IsStopped()
		}
	case <-r.IsStopped():
		// If we exited receiverLoop it means there is a connection error, we cannot
		// read messages anymore. We need to start over.

		stopSender()
		<-c.sender.IsStopped()
	}
}

// useProxy sets the websocket dialer to use the passed proxy URL.
// If the proxy has no schema http is used.
// This method is not thread safe and must be called before c.dialer is used.
func (c *wsClient) useProxy(proxy string, headers http.Header, cfg *tls.Config) error {
	proxyURL, err := url.Parse(proxy)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" { // error or bad URL - try to use http as scheme to resolve
		proxyURL, err = url.Parse("http://" + proxy)
		if err != nil {
			return err
		}
	}
	if proxyURL.Hostname() == "" {
		return url.InvalidHostError(proxy)
	}

	// Clear previous settings
	c.dialer.Proxy = nil
	c.dialer.NetDialContext = nil
	c.dialer.NetDialTLSContext = nil

	switch strings.ToLower(proxyURL.Scheme) {
	case "http":
		// FIXME: dialer.NetDialContext is currently used as a work around instead of setting dialer.Proxy as gorilla/websockets does not have 1st class support for setting proxy connect headers
		// Once http://github.com/gorilla/websocket/issues/479 is complete, we should use dialer.Proxy, and dialer.ProxyConnectHeader
		if len(headers) > 0 {
			dialer, err := dialer.NewProxyConnectDialer(proxyURL, &net.Dialer{}, dialer.WithProxyConnectHeaders(headers))
			if err != nil {
				return err
			}
			c.dialer.NetDialContext = dialer.DialContext
			return nil
		}
		c.dialer.Proxy = http.ProxyURL(proxyURL) // No connect headers, use a regular proxy
	case "https":
		if len(headers) > 0 {
			dialer, err := dialer.NewProxyConnectDialer(proxyURL, &net.Dialer{}, dialer.WithTLS(cfg), dialer.WithProxyConnectHeaders(headers))
			if err != nil {
				return err
			}
			c.dialer.NetDialTLSContext = dialer.DialContext
			return nil
		}
		c.dialer.Proxy = http.ProxyURL(proxyURL) // No connect headers, use a regular proxy
	default: // catches socks5
		c.dialer.Proxy = http.ProxyURL(proxyURL)
	}
	return nil
}
