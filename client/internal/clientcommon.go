package internal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var (
	ErrAgentDescriptionMissing      = errors.New("AgentDescription is nil")
	ErrAgentDescriptionNoAttributes = errors.New("AgentDescription has no attributes defined")
	ErrAgentHealthMissing           = errors.New("AgentHealth is nil")
	ErrReportsEffectiveConfigNotSet = errors.New("ReportsEffectiveConfig capability is not set")
	ErrReportsRemoteConfigNotSet    = errors.New("ReportsRemoteConfig capability is not set")
	ErrPackagesStateProviderNotSet  = errors.New("PackagesStateProvider must be set")
	ErrAcceptsPackagesNotSet        = errors.New("AcceptsPackages and ReportsPackageStatuses must be set")

	errAlreadyStarted               = errors.New("already started")
	errCannotStopNotStarted         = errors.New("cannot stop because not started")
	errReportsPackageStatusesNotSet = errors.New("ReportsPackageStatuses capability is not set")
)

// CallbacksWrapper wraps Callbacks such that it is possible to query if any callback
// function is in progress (called, but not yet returned). This is necessary for
// safe handling of certain ClientCommon methods when they are called from the callbacks.
// See for example Stop() implementation.
type CallbacksWrapper struct {
	wrapped types.Callbacks
	// Greater than zero if currently processing a callback.
	inCallback *int64
}

func (cc *CallbacksWrapper) OnConnect() {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	cc.wrapped.OnConnect()
}

func (cc *CallbacksWrapper) OnConnectFailed(err error) {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	cc.wrapped.OnConnectFailed(err)
}

func (cc *CallbacksWrapper) OnError(err *protobufs.ServerErrorResponse) {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	cc.wrapped.OnError(err)
}

func (cc *CallbacksWrapper) OnMessage(ctx context.Context, msg *types.MessageData) {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	cc.wrapped.OnMessage(ctx, msg)
}

func (cc *CallbacksWrapper) OnOpampConnectionSettings(
	ctx context.Context, settings *protobufs.OpAMPConnectionSettings,
) error {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	return cc.wrapped.OnOpampConnectionSettings(ctx, settings)
}

func (cc *CallbacksWrapper) OnOpampConnectionSettingsAccepted(settings *protobufs.OpAMPConnectionSettings) {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	cc.wrapped.OnOpampConnectionSettingsAccepted(settings)
}

func (cc *CallbacksWrapper) SaveRemoteConfigStatus(ctx context.Context, status *protobufs.RemoteConfigStatus) {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	cc.wrapped.SaveRemoteConfigStatus(ctx, status)
}

func (cc *CallbacksWrapper) GetEffectiveConfig(ctx context.Context) (*protobufs.EffectiveConfig, error) {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	return cc.wrapped.GetEffectiveConfig(ctx)
}

func (cc *CallbacksWrapper) OnCommand(command *protobufs.ServerToAgentCommand) error {
	cc.EnterCallback()
	defer cc.LeaveCallback()
	return cc.wrapped.OnCommand(command)
}

var _ types.Callbacks = (*CallbacksWrapper)(nil)

func NewCallbacksWrapper(wrapped types.Callbacks) *CallbacksWrapper {
	zero := int64(0)

	if wrapped == nil {
		// Make sure it is always safe to call Callbacks.
		wrapped = types.CallbacksStruct{}
	}

	return &CallbacksWrapper{
		wrapped:    wrapped,
		inCallback: &zero,
	}
}

func (cc *CallbacksWrapper) EnterCallback() {
	atomic.AddInt64(cc.inCallback, 1)
}

func (cc *CallbacksWrapper) LeaveCallback() {
	atomic.AddInt64(cc.inCallback, -1)
}

func (cc *CallbacksWrapper) InCallback() bool {
	return atomic.LoadInt64(cc.inCallback) != 0
}

// ClientCommon contains the OpAMP logic that is common between WebSocket and
// plain HTTP transports.
type ClientCommon struct {
	Logger    types.Logger
	Callbacks *CallbacksWrapper

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

// NewClientCommon creates a new ClientCommon.
func NewClientCommon(logger types.Logger, sender Sender) ClientCommon {
	return ClientCommon{
		Logger: logger, sender: sender, stoppedSignal: make(chan struct{}, 1),
	}
}

// PrepareStart prepares the client state for the next Start() call.
// It returns an error if the client is already started, or if the settings are invalid.
func (c *ClientCommon) PrepareStart(
	_ context.Context, settings types.StartSettings,
) error {
	if c.isStarted {
		return errAlreadyStarted
	}

	c.Capabilities = settings.Capabilities

	// According to OpAMP spec this capability MUST be set, since all Agents MUST report status.
	c.Capabilities |= protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus

	if c.ClientSyncedState.AgentDescription() == nil {
		return ErrAgentDescriptionMissing
	}

	if c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth != 0 && c.ClientSyncedState.Health() == nil {
		return ErrAgentHealthMissing
	}

	// Prepare remote config status.
	if settings.RemoteConfigStatus == nil {
		// RemoteConfigStatus is not provided. Start with empty.
		settings.RemoteConfigStatus = &protobufs.RemoteConfigStatus{
			Status: protobufs.RemoteConfigStatuses_RemoteConfigStatuses_UNSET,
		}
	}

	if err := c.ClientSyncedState.SetRemoteConfigStatus(settings.RemoteConfigStatus); err != nil {
		return err
	}

	// Prepare package statuses.
	c.PackagesStateProvider = settings.PackagesStateProvider
	var packageStatuses *protobufs.PackageStatuses
	if settings.PackagesStateProvider != nil {
		if c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages == 0 ||
			c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses == 0 {
			return ErrAcceptsPackagesNotSet
		}

		// Set package status from the value previously saved in the PackagesStateProvider.
		var err error
		packageStatuses, err = settings.PackagesStateProvider.LastReportedStatuses()
		if err != nil {
			return err
		}
	} else {
		if c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages != 0 ||
			c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses != 0 {
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
	c.Callbacks = NewCallbacksWrapper(settings.Callbacks)

	if err := c.sender.SetInstanceUid(settings.InstanceUid); err != nil {
		return err
	}

	return nil
}

// Stop stops the client. It returns an error if the client is not started.
func (c *ClientCommon) Stop(ctx context.Context) error {
	if !c.isStarted {
		return errCannotStopNotStarted
	}

	c.isStoppingMutex.Lock()
	cancelFunc := c.runCancel
	c.isStoppingFlag = true
	c.isStoppingMutex.Unlock()

	cancelFunc()

	if c.Callbacks.InCallback() {
		// Stop() is called from a callback. We cannot wait and block here
		// because the c.stoppedSignal may not be set until the callback
		// returns. This is the case for example when OnMessage callback is
		// called. So, for this case we return immediately and the caller
		// needs to be aware that Stop() does not wait for stopping to
		// finish in this case.
		return nil
	}

	// Wait until stopping is finished.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stoppedSignal:
	}
	return nil
}

// IsStopping returns true if Stop() was called.
func (c *ClientCommon) IsStopping() bool {
	c.isStoppingMutex.RLock()
	defer c.isStoppingMutex.RUnlock()
	return c.isStoppingFlag
}

// StartConnectAndRun initiates the connection with the Server and starts the
// background goroutine that handles the communication unitl client is stopped.
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
	c.isStarted = true

	go func() {
		defer func() {
			// We only return from runner() when we are instructed to stop.
			// When returning signal that we stopped.
			c.stoppedSignal <- struct{}{}
		}()

		runner(runCtx)
	}()
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
			msg.Capabilities = uint64(c.Capabilities)
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
	if c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig == 0 {
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

// SetRemoteConfigStatus sends a status update to the Server if the new RemoteConfigStatus
// is different from the status we already have in the state.
// It also remembers the new RemoteConfigStatus in the client state so that it can be
// sent to the Server when the Server asks for it.
func (c *ClientCommon) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	if c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig == 0 {
		return ErrReportsRemoteConfigNotSet
	}

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

// SetPackageStatuses sends a status update to the Server if the new PackageStatuses
// are different from the ones we already have in the state.
// It also remembers the new PackageStatuses in the client state so that it can be
// sent to the Server when the Server asks for it.
func (c *ClientCommon) SetPackageStatuses(statuses *protobufs.PackageStatuses) error {
	if c.Capabilities&protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses == 0 {
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
