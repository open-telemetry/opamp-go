package internal

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var (
	errAgentDescriptionMissing   = errors.New("AgentDescription is not set")
	errRemoteConfigStatusMissing = errors.New("RemoteConfigStatus is not set")
	errAlreadyStarted            = errors.New("already started")
	errCannotStopNotStarted      = errors.New("cannot stop because not started")
)

// ClientCommon contains the OpAMP logic that is common between WebSocket and
// plain HTTP transports.
type ClientCommon struct {
	Logger    types.Logger
	Callbacks types.Callbacks

	// Client state storage. This is needed if the Server asks to report the state.
	ClientSyncedState ClientSyncedState

	// The transport-specific sender.
	sender Sender

	// True if Start() is successful.
	isStarted bool

	// Cancellation func for background go routines.
	runCancel context.CancelFunc

	// True when stopping is in progress.
	isStoppingFlag  bool
	isStoppingMutex sync.RWMutex

	// Indicates that the Client is fully stopped.
	stoppedSignal chan struct{}
}

func NewClientCommon(logger types.Logger, sender Sender) ClientCommon {
	return ClientCommon{Logger: logger, sender: sender, stoppedSignal: make(chan struct{}, 1)}
}

func (c *ClientCommon) PrepareStart(_ context.Context, settings types.StartSettings) error {
	if c.isStarted {
		return errAlreadyStarted
	}

	if err := c.ClientSyncedState.SetAgentDescription(settings.AgentDescription); err != nil {
		return err
	}

	if settings.RemoteConfigStatus == nil {
		// RemoteConfigStatus is not provided. Start with empty.
		settings.RemoteConfigStatus = &protobufs.RemoteConfigStatus{
			Status: protobufs.RemoteConfigStatus_UNSET,
		}
		calcHashRemoteConfigStatus(settings.RemoteConfigStatus)
	}

	if err := c.ClientSyncedState.SetRemoteConfigStatus(settings.RemoteConfigStatus); err != nil {
		return err
	}

	c.Callbacks = settings.Callbacks
	if c.Callbacks == nil {
		// Make sure it is always safe to call Callbacks.
		c.Callbacks = types.CallbacksStruct{}
	}

	if err := c.sender.SetInstanceUid(settings.InstanceUid); err != nil {
		return err
	}

	return nil
}

func (c *ClientCommon) Stop(ctx context.Context) error {
	if !c.isStarted {
		return errCannotStopNotStarted
	}

	c.isStoppingMutex.Lock()
	cancelFunc := c.runCancel
	c.isStoppingFlag = true
	c.isStoppingMutex.Unlock()

	cancelFunc()

	// Wait until stopping is finished.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stoppedSignal:
	}
	return nil
}

func (c *ClientCommon) IsStopping() bool {
	c.isStoppingMutex.RLock()
	defer c.isStoppingMutex.RUnlock()
	return c.isStoppingFlag
}

func (c *ClientCommon) StartConnectAndRun(runner func(ctx context.Context)) {
	// Create a cancellable context.
	runCtx, runCancel := context.WithCancel(context.Background())

	c.isStoppingMutex.Lock()
	defer c.isStoppingMutex.Unlock()

	if c.isStoppingFlag {
		// Stop() was called. Don't connect.
		runCancel()
		return
	}
	c.runCancel = runCancel

	go func() {
		defer func() {
			// We only return from runner() when we are instructed to stop.
			// When returning signal that we stopped.
			c.stoppedSignal <- struct{}{}
		}()

		runner(runCtx)
	}()

	c.isStarted = true
}

// PrepareFirstMessage prepares the initial state of NextMessage struct that client
// sends when it first establishes a connection with the server.
func (c *ClientCommon) PrepareFirstMessage(ctx context.Context) error {
	cfg, err := c.Callbacks.GetEffectiveConfig(ctx)
	if err != nil {
		return err
	}
	if cfg != nil && len(cfg.Hash) == 0 {
		return errors.New("EffectiveConfig hash is empty")
	}

	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			if msg.StatusReport == nil {
				msg.StatusReport = &protobufs.StatusReport{}
			}
			msg.StatusReport.AgentDescription = c.ClientSyncedState.AgentDescription()

			msg.StatusReport.EffectiveConfig = cfg

			msg.StatusReport.RemoteConfigStatus = c.ClientSyncedState.RemoteConfigStatus()

			if msg.PackageStatuses == nil {
				msg.PackageStatuses = &protobufs.PackageStatuses{}
			}

			// TODO: set PackageStatuses.ServerProvidedAllPackagesHash field and
			// handle the Hashes for PackageStatuses properly.
		},
	)
	return nil
}

// SetAgentDescription sends a status update to the Server with the new AgentDescription
// and remembers the AgentDescription in the client state so that it can be sent
// to the Server when the Server asks for it.
func (c *ClientCommon) SetAgentDescription(descr *protobufs.AgentDescription) error {
	// store the agent description to send on reconnect
	if err := c.ClientSyncedState.SetAgentDescription(descr); err != nil {
		return err
	}
	c.sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
		statusReport.AgentDescription = descr
	})
	c.sender.ScheduleSend()
	return nil
}

// UpdateEffectiveConfig fetches the current local effective config using
// GetEffectiveConfig callback and sends it to the Server using provided Sender.
func (c *ClientCommon) UpdateEffectiveConfig(ctx context.Context) error {
	// Fetch the locally stored config.
	cfg, err := c.Callbacks.GetEffectiveConfig(ctx)
	if err != nil {
		return fmt.Errorf("GetEffectiveConfig failed: %w", err)
	}
	if cfg != nil && len(cfg.Hash) == 0 {
		return errors.New("hash field must be set, use CalcHashEffectiveConfig")
	}
	// Send it to the server.
	c.sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
		statusReport.EffectiveConfig = cfg
	})
	c.sender.ScheduleSend()

	// Note that we do not store the EffectiveConfig anywhere else. It will be deleted
	// from NextMessage when the message is sent. This avoids storing EffectiveConfig
	// in memory for longer than it is needed.
	return nil
}
