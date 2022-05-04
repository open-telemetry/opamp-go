package internal

import (
	"context"
	"errors"
	"fmt"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var (
	errAgentDescriptionMissing = errors.New("AgentDescription is not set")
)

// This file contains the OpAMP logic that is common between WebSocket and
// plain HTTP transports.
//
// TODO: introduce a CommonClient struct that encapsulates this logic and
// embed it in the HTTPClient and WSClient to avoid passing so many parameters
// to these functions. There is more common OpAMP logic that we refactor and
// move here.

// PrepareFirstMessage prepares the initial state of NextMessage struct that client
// sends when it first establishes a connection with the server.
func PrepareFirstMessage(
	ctx context.Context,
	nextMessage *NextMessage,
	clientSyncedState *ClientSyncedState,
	callbacks types.Callbacks,
) error {
	cfg, err := callbacks.GetEffectiveConfig(ctx)
	if err != nil {
		return err
	}
	if cfg != nil && len(cfg.Hash) == 0 {
		return errors.New("EffectiveConfig hash is empty")
	}

	nextMessage.Update(
		func(msg *protobufs.AgentToServer) {
			if msg.StatusReport == nil {
				msg.StatusReport = &protobufs.StatusReport{}
			}
			msg.StatusReport.AgentDescription = clientSyncedState.AgentDescription()

			msg.StatusReport.EffectiveConfig = cfg

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
func SetAgentDescription(sender Sender, clientSyncedState *ClientSyncedState, descr *protobufs.AgentDescription) error {
	// store the agent description to send on reconnect
	if err := clientSyncedState.SetAgentDescription(descr); err != nil {
		return err
	}
	sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
		statusReport.AgentDescription = descr
	})
	sender.ScheduleSend()
	return nil
}

// UpdateEffectiveConfig fetches the current local effective config using
// GetEffectiveConfig callback and sends it to the Server using provided Sender.
func UpdateEffectiveConfig(ctx context.Context, sender Sender, callbacks types.Callbacks) error {
	// Fetch the locally stored config.
	cfg, err := callbacks.GetEffectiveConfig(ctx)
	if err != nil {
		return fmt.Errorf("GetEffectiveConfig failed: %w", err)
	}
	if cfg != nil && len(cfg.Hash) == 0 {
		return errors.New("hash field must be set, use CalcHashEffectiveConfig")
	}
	// Send it to the server.
	sender.NextMessage().UpdateStatus(func(statusReport *protobufs.StatusReport) {
		statusReport.EffectiveConfig = cfg
	})
	sender.ScheduleSend()

	// Note that we do not store the EffectiveConfig anywhere else. It will be deleted
	// from NextMessage when the message is sent. This avoids storing EffectiveConfig
	// in memory for longer than it is needed.
	return nil
}
