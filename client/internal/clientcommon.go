package internal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

var (
	ErrAgentDescriptionMissing      = errors.New("AgentDescription is nil")
	ErrAgentDescriptionNoAttributes = errors.New("AgentDescription has no attributes defined")

	errAlreadyStarted       = errors.New("already started")
	errCannotStopNotStarted = errors.New("cannot stop because not started")
)

// ClientCommon contains the OpAMP logic that is common between WebSocket and
// plain HTTP transports.
type ClientCommon struct {
	Logger    types.Logger
	Callbacks types.Callbacks

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

	if c.ClientSyncedState.AgentDescription() == nil {
		return ErrAgentDescriptionMissing
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
		// Set package status from the value previously saved in the PackagesStateProvider.
		var err error
		packageStatuses, err = settings.PackagesStateProvider.LastReportedStatuses()
		if err != nil {
			return err
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
	if cfg != nil {
		calcHashEffectiveConfig(cfg)
	}

	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.AgentDescription = c.ClientSyncedState.AgentDescription()
			msg.EffectiveConfig = cfg
			msg.RemoteConfigStatus = c.ClientSyncedState.RemoteConfigStatus()
			msg.PackageStatuses = c.ClientSyncedState.PackageStatuses()

			if c.PackagesStateProvider != nil {
				// We have a state provider, so package related capabilities can work.
				msg.Capabilities |= protobufs.AgentCapabilities_AcceptsPackages
				msg.Capabilities |= protobufs.AgentCapabilities_ReportsPackageStatuses
			}
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

// calcHashEffectiveConfig calculates and sets the Hash field from the rest of the
// fields in the message.
func calcHashEffectiveConfig(msg *protobufs.EffectiveConfig) {
	cfgMap := msg.GetConfigMap().GetConfigMap()

	// Construct hash
	h := sha256.New()

	// If the config is empty don't attemp to add more to the hash
	if len(cfgMap) > 0 {
		// Sort keys of configMap to make deterministic hash
		keys := make([]string, 0, len(cfgMap))
		for k := range cfgMap {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		if msg.ConfigMap != nil {
			for _, k := range keys {
				v := cfgMap[k]
				h.Write([]byte(k))
				h.Write(v.Body)
				h.Write([]byte(v.ContentType))
			}
		}
	}
	msg.Hash = h.Sum(nil)
}

// UpdateEffectiveConfig fetches the current local effective config using
// GetEffectiveConfig callback and sends it to the Server using provided Sender.
func (c *ClientCommon) UpdateEffectiveConfig(ctx context.Context) error {
	// Fetch the locally stored config.
	cfg, err := c.Callbacks.GetEffectiveConfig(ctx)
	if err != nil {
		return fmt.Errorf("GetEffectiveConfig failed: %w", err)
	}
	if cfg != nil {
		calcHashEffectiveConfig(cfg)
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

	// Get the hash of the status before we update it.
	prevHash := c.ClientSyncedState.RemoteConfigStatus().GetHash()

	// Remember the new status.
	if err := c.ClientSyncedState.SetRemoteConfigStatus(status); err != nil {
		return err
	}

	// Check if the new status is different from the previous by comparing the hashes.
	if !bytes.Equal(prevHash, status.Hash) {
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
	if statuses.ServerProvidedAllPackagesHash == nil {
		return errServerProvidedAllPackagesHashNil
	}

	// Get the hash of the status before we update it.
	prevHash := c.ClientSyncedState.PackageStatuses().GetHash()

	if err := c.ClientSyncedState.SetPackageStatuses(statuses); err != nil {
		return err
	}

	// Check if the new status is different from the previous by comparing the hashes.
	if !bytes.Equal(prevHash, statuses.Hash) {
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
