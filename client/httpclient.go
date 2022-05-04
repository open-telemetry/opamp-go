package client

import (
	"context"
	"net/url"
	"sync"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// httpClient is an OpAMP Client implementation for plain HTTP transport.
// See specification: https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#plain-http-transport
type httpClient struct {
	logger   types.Logger
	settings StartSettings

	// OpAMP Server URL.
	url *url.URL

	// True if Start() is successful.
	isStarted bool

	// Cancellation func for background go routines.
	runCancel context.CancelFunc

	// True when stopping is in progress.
	isStoppingFlag  bool
	isStoppingMutex sync.RWMutex

	// Indicates that the Client is fully stopped.
	stoppedSignal chan struct{}

	// The looper performs HTTP request/response loop.
	looper *internal.HTTPLooper

	// Client state storage. This is needed if the Server asks to report the state.
	clientSyncedState internal.ClientSyncedState
}

var _ OpAMPClient = (*httpClient)(nil)

func NewHTTP(logger types.Logger) *httpClient {
	if logger == nil {
		logger = &sharedinternal.NopLogger{}
	}

	w := &httpClient{
		logger:        logger,
		stoppedSignal: make(chan struct{}, 1),
	}
	w.looper = internal.NewHTTPLooper(logger, &w.clientSyncedState)

	return w
}

func (w *httpClient) Start(ctx context.Context, settings StartSettings) error {
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

	if err := w.looper.SetInstanceUid(w.settings.InstanceUid); err != nil {
		return err
	}

	// Prepare server connection settings.
	var err error
	w.url, err = url.Parse(w.settings.OpAMPServerURL)
	if err != nil {
		return err
	}

	if w.settings.TLSConfig != nil {
		w.url.Scheme = "https"
	}

	if w.settings.AuthorizationHeader != "" {
		w.looper.SetRequestHeader("Authorization", w.settings.AuthorizationHeader)
	}

	// Prepare the first message to send.
	err = internal.PrepareFirstMessage(ctx, w.looper.NextMessage(), &w.clientSyncedState, w.settings.Callbacks)
	if err != nil {
		return err
	}

	w.looper.ScheduleSend()

	w.startConnectAndRun()

	w.isStarted = true

	return nil
}

func (w *httpClient) Stop(ctx context.Context) error {
	if !w.isStarted {
		return errCannotStopNotStarted
	}

	w.isStoppingMutex.Lock()
	cancelFunc := w.runCancel
	w.isStoppingFlag = true
	w.isStoppingMutex.Unlock()

	cancelFunc()

	// Wait until stopping is finished.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.stoppedSignal:
	}
	return nil
}

func (w *httpClient) SetAgentDescription(descr *protobufs.AgentDescription) error {
	return internal.SetAgentDescription(w.looper, &w.clientSyncedState, descr)
}

func (w *httpClient) UpdateEffectiveConfig(ctx context.Context) error {
	return internal.UpdateEffectiveConfig(ctx, w.looper, w.settings.Callbacks)
}

func (w *httpClient) isStopping() bool {
	w.isStoppingMutex.RLock()
	defer w.isStoppingMutex.RUnlock()
	return w.isStoppingFlag
}

func (w *httpClient) startConnectAndRun() {
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

func (w *httpClient) runUntilStopped(ctx context.Context) {

	defer func() {
		// We only return from runUntilStopped when we are instructed to stop.
		// When returning signal that we stopped.
		w.stoppedSignal <- struct{}{}
	}()

	// Start the HTTP looper. This will make request/responses with retries for
	// failures and will wait with configured polling interval if there is nothing
	// to send.
	w.looper.Run(ctx, w.settings.OpAMPServerURL, w.settings.Callbacks)
}
