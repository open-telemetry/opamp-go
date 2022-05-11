package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gorilla/websocket"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var (
	errAlreadyStarted       = errors.New("already started")
	errCannotStopNotStarted = errors.New("cannot stop because not started")
)

// wsClient is an OpAMP Client implementation for WebSocket transport.
// See specification: https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#websocket-transport
type wsClient struct {
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
	sender *internal.WSSender

	// Client state storage. This is needed if the Server asks to report the state.
	clientSyncedState internal.ClientSyncedState
}

var _ OpAMPClient = (*wsClient)(nil)

func NewWebSocket(logger types.Logger) *wsClient {
	if logger == nil {
		logger = &sharedinternal.NopLogger{}
	}

	w := &wsClient{
		logger:        logger,
		stoppedSignal: make(chan struct{}, 1),
		sender:        internal.NewSender(logger),
	}
	return w
}

func (w *wsClient) Start(_ context.Context, settings StartSettings) error {
	if w.isStarted {
		return errAlreadyStarted
	}

	if err := w.clientSyncedState.SetAgentDescription(settings.AgentDescription); err != nil {
		return err
	}

	w.settings = settings
	if w.settings.Callbacks == nil {
		// Make sure it is always safe to call Callbacks.
		w.settings.Callbacks = types.CallbacksStruct{}
	}

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

	w.startConnectAndRun()

	w.isStarted = true

	return nil
}

func (w *wsClient) Stop(ctx context.Context) error {
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
		_ = conn.Close()
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

func (w *wsClient) SetAgentDescription(descr *protobufs.AgentDescription) error {
	return internal.SetAgentDescription(w.sender, &w.clientSyncedState, descr)
}

func (w *wsClient) UpdateEffectiveConfig(ctx context.Context) error {
	return internal.UpdateEffectiveConfig(ctx, w.sender, w.settings.Callbacks)
}

// Try to connect once. Returns an error if connection fails and optional retryAfter
// duration to indicate to the caller to retry after the specified time as instructed
// by the server.
func (w *wsClient) tryConnectOnce(ctx context.Context) (err error, retryAfter sharedinternal.OptionalDuration) {
	var resp *http.Response
	conn, resp, err := w.dialer.DialContext(ctx, w.url.String(), w.requestHeader)
	if err != nil {
		if w.settings.Callbacks != nil {
			w.settings.Callbacks.OnConnectFailed(err)
		}
		if resp != nil {
			w.logger.Errorf("Server responded with status=%v", resp.Status)
			duration := sharedinternal.ExtractRetryAfterHeader(resp)
			return err, duration
		}
		return err, sharedinternal.OptionalDuration{Defined: false}
	}

	// Successfully connected.
	w.connMutex.Lock()
	w.conn = conn
	w.connMutex.Unlock()
	if w.settings.Callbacks != nil {
		w.settings.Callbacks.OnConnect()
	}

	return nil, sharedinternal.OptionalDuration{Defined: false}
}

// Continuously try until connected. Will return nil when successfully
// connected. Will return error if it is cancelled via context.
func (w *wsClient) ensureConnected(ctx context.Context) error {
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

					if retryAfter.Defined && retryAfter.Duration > interval {
						// If the server suggested connecting later than our interval
						// then honour server's request, otherwise wait at least
						// as much as we calculated.
						interval = retryAfter.Duration
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

func (w *wsClient) isStopping() bool {
	w.isStoppingMutex.RLock()
	defer w.isStoppingMutex.RUnlock()
	return w.isStoppingFlag
}

func (w *wsClient) startConnectAndRun() {
	// Create a cancellable context.
	runCtx, runCancel := context.WithCancel(context.Background())

	w.isStoppingMutex.Lock()
	defer w.isStoppingMutex.Unlock()

	if w.isStoppingFlag {
		// Stop() was called. Don't connect.
		runCancel()
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
func (w *wsClient) runOneCycle(ctx context.Context) {
	if err := w.ensureConnected(ctx); err != nil {
		// Can't connect, so can't move forward. This currently happens when we
		// are being stopped.
		return
	}

	if w.isStopping() {
		_ = w.conn.Close()
		return
	}

	// Prepare the first status report.
	err := internal.PrepareFirstMessage(ctx, w.sender.NextMessage(), &w.clientSyncedState, w.settings.Callbacks)
	if err != nil {
		w.logger.Errorf("cannot prepare the first message:%v", err)
		return
	}

	// Create a cancellable context for background processors.
	procCtx, procCancel := context.WithCancel(ctx)

	// Connected successfully. Start the sender. This will also send the first
	// status report.
	if err := w.sender.Start(procCtx, w.settings.InstanceUid, w.conn); err != nil {
		w.logger.Errorf("Failed to send first status report: %v", err)
		// We could not send the report, the only thing we can do is start over.
		_ = w.conn.Close()
		procCancel()
		return
	}

	// First status report sent. Now loop to receive and process messages.
	r := internal.NewWSReceiver(w.logger, w.settings.Callbacks, w.conn, w.sender, &w.clientSyncedState)
	r.ReceiverLoop(ctx)

	// Stop the background processors.
	procCancel()

	// If we exited receiverLoop it means there is a connection error, we cannot
	// read messages anymore. We need to start over.

	// Close the connection to unblock the WSSender as well.
	_ = w.conn.Close()

	// Wait for WSSender to stop.
	w.sender.WaitToStop()
}

func (w *wsClient) runUntilStopped(ctx context.Context) {

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
