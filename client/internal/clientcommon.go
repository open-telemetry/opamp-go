package internal

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

var (
	ErrAgentDescriptionMissing      = errors.New("AgentDescription is nil")
	ErrAgentDescriptionNoAttributes = errors.New("AgentDescription has no attributes defined")
	ErrAgentHealthMissing           = errors.New("AgentHealth is nil")
	ErrReportsEffectiveConfigNotSet = errors.New("ReportsEffectiveConfig capability is not set")
	ErrPackagesStateProviderNotSet  = errors.New("PackagesStateProvider must be set")
	ErrAcceptsPackagesNotSet        = errors.New("AcceptsPackages and ReportsPackageStatuses must be set")

	errAlreadyStarted               = errors.New("already started")
	errCannotStopNotStarted         = errors.New("cannot stop because not started")
	errReportsPackageStatusesNotSet = errors.New("ReportsPackageStatuses capability is not set")
)

// ClientCommon contains the OpAMP logic that is common between WebSocket and
// plain HTTP transports.
type ClientCommon struct {
	Logger    types.Logger
	Callbacks types.Callbacks

	// Agent's capabilities defined at Start() time.
	Capabilities protobufs.AgentCapabilities

	// Client state storage. This is needed if the Server asks to report the state.
	ClientSyncedState ClientSyncedState

	// PackagesStateProvider provides access to the local state of packages.
	PackagesStateProvider types.PackagesStateProvider

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
	return ClientCommon{
		Logger: logger, sender: sender, stoppedSignal: make(chan struct{}, 1),
	}
}

func (c *ClientCommon) PrepareStart(
	_ context.Context, settings types.StartSettings,
) error {
	if c.isStarted {
		return errAlreadyStarted
	}

	c.Capabilities = settings.Capabilities

	// According to OpAMP spec this capability MUST be set, since all Agents MUST report status.
	c.Capabilities |= protobufs.AgentCapabilities_ReportsStatus

	if c.ClientSyncedState.AgentDescription() == nil {
		return ErrAgentDescriptionMissing
	}

	if c.Capabilities&protobufs.AgentCapabilities_ReportsHealth != 0 && c.ClientSyncedState.Health() == nil {
		return ErrAgentHealthMissing
	}

	// Prepare remote config status.
	if settings.RemoteConfigStatus == nil {
		// RemoteConfigStatus is not provided. Start with empty.
		settings.RemoteConfigStatus = &protobufs.RemoteConfigStatus{
			Status: protobufs.RemoteConfigStatus_UNSET,
		}
	}

	if err := c.ClientSyncedState.SetRemoteConfigStatus(settings.RemoteConfigStatus); err != nil {
		return err
	}

	// Prepare package statuses.
	c.PackagesStateProvider = settings.PackagesStateProvider
	var packageStatuses *protobufs.PackageStatuses
	if settings.PackagesStateProvider != nil {
		if c.Capabilities&protobufs.AgentCapabilities_AcceptsPackages == 0 ||
			c.Capabilities&protobufs.AgentCapabilities_ReportsPackageStatuses == 0 {
			return ErrAcceptsPackagesNotSet
		}

		// Set package status from the value previously saved in the PackagesStateProvider.
		var err error
		packageStatuses, err = settings.PackagesStateProvider.LastReportedStatuses()
		if err != nil {
			return err
		}
	} else {
		if c.Capabilities&protobufs.AgentCapabilities_AcceptsPackages != 0 ||
			c.Capabilities&protobufs.AgentCapabilities_ReportsPackageStatuses != 0 {
			return ErrPackagesStateProviderNotSet
		}
	}

	if packageStatuses == nil {
		// PackageStatuses is not provided. Start with empty.
		packageStatuses = &protobufs.PackageStatuses{}
	}
	if err := c.ClientSyncedState.SetPackageStatuses(packageStatuses); err != nil {
		return err
	}

	// Prepare callbacks.
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
// sends when it first establishes a connection with the Server.
func (c *ClientCommon) PrepareFirstMessage(ctx context.Context) error {
	cfg, err := c.Callbacks.GetEffectiveConfig(ctx)
	if err != nil {
		return err
	}

	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.AgentDescription = c.ClientSyncedState.AgentDescription()
			msg.EffectiveConfig = cfg
			msg.RemoteConfigStatus = c.ClientSyncedState.RemoteConfigStatus()
			msg.PackageStatuses = c.ClientSyncedState.PackageStatuses()
			msg.Capabilities = c.Capabilities
		},
	)
	return nil
}

// AgentDescription returns the current state of the AgentDescription.
func (c *ClientCommon) AgentDescription() *protobufs.AgentDescription {
	// Return a cloned copy to allow caller to do whatever they want with the result.
	return proto.Clone(c.ClientSyncedState.AgentDescription()).(*protobufs.AgentDescription)
}

// SetAgentDescription sends a status update to the Server with the new AgentDescription
// and remembers the AgentDescription in the client state so that it can be sent
// to the Server when the Server asks for it.
func (c *ClientCommon) SetAgentDescription(descr *protobufs.AgentDescription) error {
	// store the Agent description to send on reconnect
	if err := c.ClientSyncedState.SetAgentDescription(descr); err != nil {
		return err
	}
	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.AgentDescription = c.ClientSyncedState.AgentDescription()
		},
	)
	c.sender.ScheduleSend()
	return nil
}

// SetHealth sends a status update to the Server with the new AgentHealth
// and remembers the AgentHealth in the client state so that it can be sent
// to the Server when the Server asks for it.
func (c *ClientCommon) SetHealth(health *protobufs.AgentHealth) error {
	// store the AgentHealth to send on reconnect
	if err := c.ClientSyncedState.SetHealth(health); err != nil {
		return err
	}
	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.Health = c.ClientSyncedState.Health()
		},
	)
	c.sender.ScheduleSend()
	return nil
}

// UpdateEffectiveConfig fetches the current local effective config using
// GetEffectiveConfig callback and sends it to the Server using provided Sender.
func (c *ClientCommon) UpdateEffectiveConfig(ctx context.Context) error {
	if c.Capabilities&protobufs.AgentCapabilities_ReportsEffectiveConfig == 0 {
		return ErrReportsEffectiveConfigNotSet
	}

	// Fetch the locally stored config.
	cfg, err := c.Callbacks.GetEffectiveConfig(ctx)
	if err != nil {
		return fmt.Errorf("GetEffectiveConfig failed: %w", err)
	}

	// Send it to the Server.
	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.EffectiveConfig = cfg
		},
	)
	// TODO: if this call is coming from OnMessage callback don't schedule the send
	// immediately, wait until the end of OnMessage to send one message only.
	c.sender.ScheduleSend()

	// Note that we do not store the EffectiveConfig anywhere else. It will be deleted
	// from NextMessage when the message is sent. This avoids storing EffectiveConfig
	// in memory for longer than it is needed.
	return nil
}

func (c *ClientCommon) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	if status.LastRemoteConfigHash == nil {
		return errLastRemoteConfigHashNil
	}

	statusChanged := !proto.Equal(c.ClientSyncedState.RemoteConfigStatus(), status)

	// Remember the new status.
	if err := c.ClientSyncedState.SetRemoteConfigStatus(status); err != nil {
		return err
	}

	if statusChanged {
		// Let the Server know about the new status.
		c.sender.NextMessage().Update(
			func(msg *protobufs.AgentToServer) {
				msg.RemoteConfigStatus = c.ClientSyncedState.RemoteConfigStatus()
			},
		)
		// TODO: if this call is coming from OnMessage callback don't schedule the send
		// immediately, wait until the end of OnMessage to send one message only.
		c.sender.ScheduleSend()
	}

	return nil
}

func (c *ClientCommon) SetPackageStatuses(statuses *protobufs.PackageStatuses) error {
	if c.Capabilities&protobufs.AgentCapabilities_ReportsPackageStatuses == 0 {
		return errReportsPackageStatusesNotSet
	}

	if statuses.ServerProvidedAllPackagesHash == nil {
		return errServerProvidedAllPackagesHashNil
	}

	statusChanged := !proto.Equal(c.ClientSyncedState.PackageStatuses(), statuses)

	if err := c.ClientSyncedState.SetPackageStatuses(statuses); err != nil {
		return err
	}

	// Check if the new status is different from the previous.
	if statusChanged {
		// Let the Server know about the new status.

		c.sender.NextMessage().Update(
			func(msg *protobufs.AgentToServer) {
				msg.PackageStatuses = c.ClientSyncedState.PackageStatuses()
			},
		)
		// TODO: if this call is coming from OnMessage callback don't schedule the send
		// immediately, wait until the end of OnMessage to send one message only.
		c.sender.ScheduleSend()
	}

	return nil
}
