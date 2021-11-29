package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gorilla/websocket"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/protobufs"
)

var (
	errAlreadyStarted       = errors.New("already started")
	errCannotStopNotStarted = errors.New("cannot stop because not started")
)

// client is a Client implementation.
type client struct {
	logger   types.Logger
	settings StartSettings

	// OpAMP Server URL.
	url *url.URL

	// HTTP request headers to use when connecting to OpAMP Server.
	requestHeader http.Header

	// Websocket dialer and connection.
	dialer    websocket.Dialer
	conn      *websocket.Conn
	connMutex sync.RWMutex

	// True if Start() is successful.
	isStarted bool

	// Cancellation func background running go routines.
	runCancel context.CancelFunc

	// True when stopping is in progress.
	isStoppingFlag  bool
	isStoppingMutex sync.RWMutex

	// Indicates that the Client is fully stopped.
	stoppedSignal chan struct{}

	// The sender is responsible for sending portion of the OpAMP protocol.
	sender *internal.Sender
}

var _ OpAMPClient = (*client)(nil)

const retryAfterHTTPHeader = "Retry-After"

func New(logger types.Logger) *client {
	if logger == nil {
		logger = &nopLogger{}
	}

	w := &client{
		logger:        logger,
		stoppedSignal: make(chan struct{}, 1),
		sender:        internal.NewSender(logger),
	}
	return w
}

func (w *client) Start(settings StartSettings) error {
	if w.isStarted {
		return errAlreadyStarted
	}

	w.settings = settings

	var err error

	// Prepare server connection settings.
	w.url, err = url.Parse(w.settings.OpAMPServerURL)
	if err != nil {
		return err
	}

	w.dialer = *websocket.DefaultDialer

	if w.settings.TLSConfig != nil {
		w.url.Scheme = "wss"
	}
	w.dialer.TLSClientConfig = w.settings.TLSConfig

	if w.settings.AuthorizationHeader != "" {
		w.requestHeader = http.Header{}
		w.requestHeader["Authorization"] = []string{w.settings.AuthorizationHeader}
	}

	// Prepare the first status report.
	w.sender.UpdateStatus(
		func(statusReport *protobufs.StatusReport) {
			statusReport.AgentDescription = &protobufs.AgentDescription{
				AgentType:    w.settings.AgentType,
				AgentVersion: w.settings.AgentVersion,
			}

			statusReport.ServerProvidedAllAddonsHash = w.settings.LastServerProvidedAllAddonsHash
		},
	)

	w.startConnectAndRun()

	w.isStarted = true

	return nil
}

func (w *client) Stop(ctx context.Context) error {
	if !w.isStarted {
		return errCannotStopNotStarted
	}

	w.isStoppingMutex.Lock()
	cancelFunc := w.runCancel
	w.isStoppingFlag = true
	w.isStoppingMutex.Unlock()

	cancelFunc()

	// Close connection if any.
	w.connMutex.RLock()
	conn := w.conn
	w.connMutex.RUnlock()

	if conn != nil {
		conn.Close()
	}
	// If we are not connected by this point we may still get connected because
	// we are racing with tryConnectOnce() func. However, after tryConnectOnce we will
	// check the isStopping flag in receiverLoop() and will exit receiverLoop, so we will
	// not deadlock.

	// Wait until stopping is finished.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.stoppedSignal:
	}
	return nil
}

func (w *client) SetAgentAttributes(attrs map[string]protobufs.AnyValue) error {
	panic("implement me")
}

func (w *client) SetEffectiveConfig(config *protobufs.EffectiveConfig) error {
	w.sender.UpdateStatus(func(statusReport *protobufs.StatusReport) {
		statusReport.EffectiveConfig = config
	})
	w.sender.ScheduleSend()
	return nil
}

type optionalDuration struct {
	duration time.Duration
	// true if duration field is defined.
	defined bool
}

// Try to connect once. Returns an error if connection fails and optional retryAfter
// duration to indicate to the caller to retry after the specified time as instructed
// by the server.
func (w *client) tryConnectOnce(ctx context.Context) (err error, retryAfter optionalDuration) {
	var resp *http.Response
	conn, resp, err := w.dialer.DialContext(ctx, w.url.String(), w.requestHeader)
	if err != nil {
		if w.settings.Callbacks != nil {
			w.settings.Callbacks.OnConnectFailed(err)
		}
		if resp != nil {
			w.logger.Errorf("Server responded with status=%v", resp.Status)
			duration := extractRetryAfterHeader(resp)
			return err, duration
		}
		return err, optionalDuration{defined: false}
	}

	// Successfully connected.
	w.connMutex.Lock()
	w.conn = conn
	w.connMutex.Unlock()
	if w.settings.Callbacks != nil {
		w.settings.Callbacks.OnConnect()
	}

	return nil, optionalDuration{defined: false}
}

// Extract Retry-After response header if the status is 503 or 429. Returns
// 0 duration if the header is not found or the status is different.
func extractRetryAfterHeader(resp *http.Response) optionalDuration {
	if resp.StatusCode == http.StatusServiceUnavailable ||
		resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := strings.TrimSpace(resp.Header.Get(retryAfterHTTPHeader))
		if retryAfter != "" {
			retryIntervalSec, err := strconv.Atoi(retryAfter)
			if err == nil {
				retryInterval := time.Duration(retryIntervalSec) * time.Second
				return optionalDuration{defined: true, duration: retryInterval}
			}
		}
	}
	return optionalDuration{defined: false}
}

// Continuously try until connected. Will return nil when successfully
// connected. Will return error if it is cancelled via context.
func (w *client) ensureConnected(ctx context.Context) error {
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
				if err, retryAfter := w.tryConnectOnce(ctx); err != nil {
					if errors.Is(err, context.Canceled) {
						w.logger.Debugf("Client is stopped, will not try anymore.")
						return err
					} else {
						w.logger.Errorf("Connection failed (%v), will retry.", err)
					}
					// Retry again a bit later.

					if retryAfter.defined && retryAfter.duration > interval {
						// If the server suggested connecting later than our interval
						// then honour server's request, otherwise wait at least
						// as much as we calculated.
						interval = retryAfter.duration
					}

					continue
				}
				// Connected successfully.
				return nil
			}

		case <-ctx.Done():
			w.logger.Debugf("Client is stopped, will not try anymore.")
			timer.Stop()
			return ctx.Err()
		}
	}
}

func (w *client) isStopping() bool {
	w.isStoppingMutex.RLock()
	defer w.isStoppingMutex.RUnlock()
	return w.isStoppingFlag
}

func (w *client) startConnectAndRun() {
	// Create a cancellable context.
	runCtx, runCancel := context.WithCancel(context.Background())

	w.isStoppingMutex.Lock()
	defer w.isStoppingMutex.Unlock()

	if w.isStoppingFlag {
		// Stop() was called. Don't connect.
		return
	}
	w.runCancel = runCancel

	go w.runUntilStopped(runCtx)
}

// runOneCycle performs the following actions:
//   1. connect (try until succeeds).
//   2. send first status report.
//   3. receive and process messages until error happens.
// If it encounters an error it closes the connection and returns.
// Will stop and return if Stop() is called (ctx is cancelled, isStopping is set).
func (w *client) runOneCycle(ctx context.Context) {
	if err := w.ensureConnected(ctx); err != nil {
		// Can't connect, so can't move forward. This currently happens when we
		// are being stopped.
		return
	}

	if w.isStopping() {
		w.conn.Close()
		return
	}

	// Create a cancellable context for background processors.
	procCtx, procCancel := context.WithCancel(ctx)

	// Connected successfully. Start the sender. This will also send the first
	// status report.
	if err := w.sender.Start(procCtx, w.settings.InstanceUid, w.conn); err != nil {
		w.logger.Errorf("Failed to send first status report: %v", err)
		// We could not send the report, the only thing we can do is start over.
		w.conn.Close()
		return
	}

	// First status report sent. Now loop to receive and process messages.
	r := internal.NewReceiver(w.logger, w.settings.Callbacks, w.conn, w.sender)
	r.ReceiverLoop(ctx)

	// Stop the background processors.
	procCancel()

	// If we exited receiverLoop it means there is a connection error, we cannot
	// read messages anymore. We need to start over.

	// Close the connection to unblock the Sender as well.
	w.conn.Close()

	// Wait for Sender to stop.
	w.sender.WaitToStop()
}

func (w *client) runUntilStopped(ctx context.Context) {

	defer func() {
		// We only return from runUntilStopped when we are instructed to stop.
		// When returning signal that we stopped.
		w.stoppedSignal <- struct{}{}
	}()

	// Iterates until we detect that the client is stopping.
	for {
		if w.isStopping() {
			return
		}

		w.runOneCycle(ctx)
	}
}
