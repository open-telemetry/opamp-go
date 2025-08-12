package internal

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

// receivedProcessor handles the processing of messages received from the Server.
type receivedProcessor struct {
	logger types.Logger

	// Callbacks to call for corresponding messages.
	callbacks types.Callbacks

	// A sender to cooperate with when the received message has an impact on
	// what will be sent later.
	sender Sender

	// Client state storage. This is needed if the Server asks to report the state.
	clientSyncedState *ClientSyncedState

	packagesStateProvider types.PackagesStateProvider

	// packageSyncMutex protects against multiple package syncing operations at the same time.
	packageSyncMutex *sync.Mutex

	// Download reporter interval value
	// a negative number indicates that the default should be used instead.
	downloadReporterInt time.Duration
}

func newReceivedProcessor(
	logger types.Logger,
	callbacks types.Callbacks,
	sender Sender,
	clientSyncedState *ClientSyncedState,
	packagesStateProvider types.PackagesStateProvider,
	packageSyncMutex *sync.Mutex,
	downloadReporterInt time.Duration,
) receivedProcessor {
	return receivedProcessor{
		logger:                logger,
		callbacks:             callbacks,
		sender:                sender,
		clientSyncedState:     clientSyncedState,
		packagesStateProvider: packagesStateProvider,
		packageSyncMutex:      packageSyncMutex,
		downloadReporterInt:   downloadReporterInt,
	}
}

// ProcessReceivedMessage is the entry point into the processing routine. It examines
// the received message and performs any processing necessary based on what fields are set.
// This function will call any relevant callbacks.
func (r *receivedProcessor) ProcessReceivedMessage(ctx context.Context, msg *protobufs.ServerToAgent) {
	// Note that anytime we add a new command capabilities we need to add a check here.
	// This is because we want to ignore commands that the agent does not have the capability
	// to process.
	if msg.Command != nil {
		if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand) {
			r.rcvCommand(ctx, msg.Command)
			// If a command message exists, other messages will be ignored
			return
		} else {
			r.logger.Debugf(ctx, "Ignoring Command, agent does not have AcceptsCommands capability")
		}
	}

	scheduled, err := r.rcvFlags(ctx, protobufs.ServerToAgentFlags(msg.Flags))
	if err != nil {
		r.logger.Errorf(ctx, "cannot processed received flags:%v", err)
	}

	msgData := &types.MessageData{}

	if msg.RemoteConfig != nil {
		if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig) {
			msgData.RemoteConfig = msg.RemoteConfig
		} else {
			r.logger.Debugf(ctx, "Ignoring RemoteConfig, agent does not have AcceptsRemoteConfig capability")
		}
	}

	if msg.ConnectionSettings != nil {
		msgData.OfferedConnectionsSettingsHash = msg.ConnectionSettings.Hash
		if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsConnectionSettingsStatus) {
			connectionStatus := &protobufs.ConnectionSettingsStatus{
				LastConnectionSettingsHash: msg.ConnectionSettings.Hash,
				Status:                     protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_FAILED,
				ErrorMessage:               "client does not support accepting connection settings",
			}
			if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnTraces) ||
				r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics) ||
				r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnLogs) ||
				r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings) ||
				r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsOtherConnectionSettings) {
				connectionStatus = &protobufs.ConnectionSettingsStatus{
					LastConnectionSettingsHash: msg.ConnectionSettings.Hash,
					Status:                     protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_APPLYING,
				}
			}
			if err := r.clientSyncedState.SetConnectionSettingsStatus(connectionStatus); err != nil {
				r.logger.Errorf(ctx, "Unable to persist connection settings status applying state: %v", err)
			}
			r.sender.NextMessage().Update(func(sendMsg *protobufs.AgentToServer) {
				sendMsg.ConnectionSettingsStatus = connectionStatus
			})
			scheduled = true // send connection setting status, if OnOpampConnectionSettings or OnConnectionSettings run synchronously, the status may be replaced.
		}
		if msg.ConnectionSettings.OwnMetrics != nil {
			if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics) {
				msgData.OwnMetricsConnSettings = msg.ConnectionSettings.OwnMetrics
			} else {
				r.logger.Debugf(ctx, "Ignoring OwnMetrics, agent does not have ReportsOwnMetrics capability")
			}
		}

		if msg.ConnectionSettings.OwnTraces != nil {
			if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnTraces) {
				msgData.OwnTracesConnSettings = msg.ConnectionSettings.OwnTraces
			} else {
				r.logger.Debugf(ctx, "Ignoring OwnTraces, agent does not have ReportsOwnTraces capability")
			}
		}

		if msg.ConnectionSettings.OwnLogs != nil {
			if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnLogs) {
				msgData.OwnLogsConnSettings = msg.ConnectionSettings.OwnLogs
			} else {
				r.logger.Debugf(ctx, "Ignoring OwnLogs, agent does not have ReportsOwnLogs capability")
			}
		}

		if msg.ConnectionSettings.OtherConnections != nil {
			if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsOtherConnectionSettings) {
				msgData.OtherConnSettings = msg.ConnectionSettings.OtherConnections
			} else {
				r.logger.Debugf(ctx, "Ignoring OtherConnections, agent does not have AcceptsOtherConnectionSettings capability")
			}
		}
	}

	if msg.PackagesAvailable != nil {
		if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages) {
			msgData.PackagesAvailable = msg.PackagesAvailable
			pkgSyncer, err := NewPackagesSyncer(
				r.logger,
				msgData.PackagesAvailable,
				r.sender,
				r.clientSyncedState,
				r.packagesStateProvider,
				r.packageSyncMutex,
				r.downloadReporterInt,
				r.callbacks.DownloadHTTPClient,
			)
			if err != nil {
				r.logger.Errorf(ctx, "failed to create package syncer: %v", err)
			} else {
				msgData.PackageSyncer = pkgSyncer
			}
		} else {
			r.logger.Debugf(ctx, "Ignoring PackagesAvailable, agent does not have AcceptsPackages capability")
		}
	}

	if msg.AgentIdentification != nil {
		err := r.rcvAgentIdentification(ctx, msg.AgentIdentification)
		if err != nil {
			r.logger.Errorf(ctx, "Failed to set agent ID: %v", err)
		} else {
			msgData.AgentIdentification = msg.AgentIdentification
		}
	}

	if msg.CustomCapabilities != nil {
		msgData.CustomCapabilities = msg.CustomCapabilities
	}

	if msg.CustomMessage != nil {
		// ensure that the agent supports the capability
		if r.clientSyncedState.HasCustomCapability(msg.CustomMessage.Capability) {
			msgData.CustomMessage = msg.CustomMessage
		} else {
			r.logger.Debugf(ctx, "Ignoring CustomMessage, agent does not have %s capability", msg.CustomMessage.Capability)
		}
	}

	r.callbacks.OnMessage(ctx, msgData)

	r.rcvOpampConnectionSettings(ctx, msg.ConnectionSettings)

	r.rcvConnectionSettings(ctx, msg.ConnectionSettings)

	if scheduled {
		r.sender.ScheduleSend()
	}

	errResponse := msg.GetErrorResponse()
	if errResponse != nil {
		r.processErrorResponse(ctx, errResponse)
	}
}

func (r *receivedProcessor) hasCapability(capability protobufs.AgentCapabilities) bool {
	return r.clientSyncedState.Capabilities()&capability != 0
}

func (r *receivedProcessor) rcvFlags(
	ctx context.Context,
	flags protobufs.ServerToAgentFlags,
) (scheduleSend bool, err error) {
	// If the Server asks to report data we fetch it from the client state storage and
	// send to the Server.

	if flags&protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState != 0 {
		cfg, err := r.callbacks.GetEffectiveConfig(ctx)
		if err != nil {
			r.logger.Errorf(ctx, "Cannot GetEffectiveConfig: %v", err)
			cfg = nil
		}

		r.sender.NextMessage().Update(
			func(msg *protobufs.AgentToServer) {
				msg.Capabilities = uint64(r.clientSyncedState.Capabilities())
				msg.AgentDescription = r.clientSyncedState.AgentDescription()
				msg.Health = r.clientSyncedState.Health()
				msg.RemoteConfigStatus = r.clientSyncedState.RemoteConfigStatus()
				msg.PackageStatuses = r.clientSyncedState.PackageStatuses()
				msg.CustomCapabilities = r.clientSyncedState.CustomCapabilities()
				msg.Flags = r.clientSyncedState.Flags()
				msg.AvailableComponents = r.clientSyncedState.AvailableComponents()

				// The logic for EffectiveConfig is similar to the previous 6 sub-messages however
				// the EffectiveConfig is fetched using GetEffectiveConfig instead of
				// from clientSyncedState. We do this to avoid keeping EffectiveConfig in-memory.
				msg.EffectiveConfig = cfg
			},
		)
		scheduleSend = true
	}

	if flags&protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportAvailableComponents != 0 {
		r.sender.NextMessage().Update(
			func(msg *protobufs.AgentToServer) {
				msg.AvailableComponents = r.clientSyncedState.AvailableComponents()
			},
		)
		scheduleSend = true
	}

	return scheduleSend, nil
}

func (r *receivedProcessor) rcvOpampConnectionSettings(ctx context.Context, settings *protobufs.ConnectionSettingsOffers) {
	if settings == nil || settings.Opamp == nil {
		return
	}

	if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat) {
		interval := time.Duration(settings.Opamp.HeartbeatIntervalSeconds) * time.Second
		if err := r.sender.SetHeartbeatInterval(interval); err != nil {
			r.logger.Errorf(ctx, "Failed to set heartbeat interval: %v", err)
		}
	}

	if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings) {
		err := r.callbacks.OnOpampConnectionSettings(ctx, settings.Opamp)
		if err != nil {
			r.logger.Errorf(ctx, "Failed to process OpAMPConnectionSettings: %v", err)
		}
		if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsConnectionSettingsStatus) {
			status := protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_APPLIED
			errMsg := ""
			if err != nil {
				status = protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_FAILED
				errMsg = err.Error()
			}

			connectionStatus := &protobufs.ConnectionSettingsStatus{
				LastConnectionSettingsHash: settings.Hash,
				Status:                     status,
				ErrorMessage:               errMsg,
			}
			oldStatus := r.clientSyncedState.ConnectionSettingsStatus()

			if !updateStoredConnectionSettingsStatus(oldStatus, connectionStatus) {
				r.logger.Debugf(ctx, "Client skipping connection status state update from %v to %v", oldStatus.GetStatus(), connectionStatus.GetStatus())
				return
			}

			if err := r.clientSyncedState.SetConnectionSettingsStatus(connectionStatus); err != nil {
				r.logger.Errorf(ctx, "Unable to persist connection settings status %s state: %v", status.String(), err)
			}
			r.sender.NextMessage().Update(func(sendMsg *protobufs.AgentToServer) {
				sendMsg.ConnectionSettingsStatus = connectionStatus
			})
			r.sender.ScheduleSend()
		}
	} else {
		r.logger.Debugf(ctx, "Ignoring Opamp, agent does not have AcceptsOpAMPConnectionSettings capability")
	}
}

func (r *receivedProcessor) rcvConnectionSettings(ctx context.Context, settings *protobufs.ConnectionSettingsOffers) {
	if settings == nil || (settings.OwnMetrics == nil &&
		settings.OwnTraces == nil &&
		settings.OwnLogs == nil &&
		settings.OtherConnections == nil) {
		return
	}

	clone := proto.Clone(settings).(*protobufs.ConnectionSettingsOffers)
	if !r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics) && clone.OwnMetrics != nil {
		clone.OwnMetrics = nil
		r.logger.Debugf(ctx, "Ignoring OwnMetrics, agent does not have ReportsOwnMetrics capability")
	}
	if !r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnTraces) && clone.OwnTraces != nil {
		clone.OwnTraces = nil
		r.logger.Debugf(ctx, "Ignoring OwnTraces, agent does not have ReportsOwnTraces capability")
	}
	if !r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnLogs) && clone.OwnLogs != nil {
		clone.OwnLogs = nil
		r.logger.Debugf(ctx, "Ignoring OwnLogs, agent does not have ReportsOwnLogs capability")
	}
	if !r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsOtherConnectionSettings) && clone.OtherConnections != nil {
		clone.OtherConnections = nil
		r.logger.Debugf(ctx, "Ignoring OtherConnections, agent does not have AcceptsOtherConnectionSettings capability")
	}

	if clone.OwnMetrics != nil || clone.OwnTraces != nil || clone.OwnLogs != nil || clone.OtherConnections != nil {
		err := r.callbacks.OnConnectionSettings(ctx, settings)
		if err != nil {
			r.logger.Errorf(ctx, "Failed to process ConnectionSettings: %v", err)
		}
		if r.hasCapability(protobufs.AgentCapabilities_AgentCapabilities_ReportsConnectionSettingsStatus) {
			status := protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_APPLIED
			errMsg := ""
			if err != nil {
				status = protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_FAILED
				errMsg = err.Error()
			}

			connectionStatus := &protobufs.ConnectionSettingsStatus{
				LastConnectionSettingsHash: settings.Hash,
				Status:                     status,
				ErrorMessage:               errMsg,
			}
			oldStatus := r.clientSyncedState.ConnectionSettingsStatus()

			if !updateStoredConnectionSettingsStatus(oldStatus, connectionStatus) {
				r.logger.Debugf(ctx, "Client skipping connection status state update from %v to %v", oldStatus.GetStatus(), connectionStatus.GetStatus())
				return
			}

			if err := r.clientSyncedState.SetConnectionSettingsStatus(connectionStatus); err != nil {
				r.logger.Errorf(ctx, "Unable to persist connection settings status %s state: %v", status.String(), err)
			}
			r.sender.NextMessage().Update(func(sendMsg *protobufs.AgentToServer) {
				sendMsg.ConnectionSettingsStatus = connectionStatus
			})
			r.sender.ScheduleSend()
		}
	} else {
		r.logger.Debugf(ctx, "Ignoring ConnectionSettings, agent does not have corresponding capability")
	}
}

func (r *receivedProcessor) processErrorResponse(ctx context.Context, body *protobufs.ServerErrorResponse) {
	if body != nil {
		r.callbacks.OnError(ctx, body)
	}
}

func (r *receivedProcessor) rcvAgentIdentification(ctx context.Context, agentId *protobufs.AgentIdentification) error {
	if len(agentId.NewInstanceUid) != 16 {
		err := fmt.Errorf("instance uid must be 16 bytes but is %d bytes long", len(agentId.NewInstanceUid))
		r.logger.Debugf(ctx, err.Error())
		return err
	}

	err := r.sender.SetInstanceUid(types.InstanceUid(agentId.NewInstanceUid))
	if err != nil {
		r.logger.Errorf(ctx, "Error while setting instance uid: %v", err)
		return err
	}

	// If we set up a new instance ID, reset the RequestInstanceUid flag.
	r.clientSyncedState.flags &^= protobufs.AgentToServerFlags_AgentToServerFlags_RequestInstanceUid

	return nil
}

func (r *receivedProcessor) rcvCommand(ctx context.Context, command *protobufs.ServerToAgentCommand) {
	if command != nil {
		r.callbacks.OnCommand(ctx, command)
	}
}

// updateStoredConnectionSettingsStatus returns a bool of if status should replace oldStatus.
// It's true if:
// - no oldStatus
// - hash changes
// - status changes from APPLYING or UNSET
// - status changes to FAILED
func updateStoredConnectionSettingsStatus(oldStatus, status *protobufs.ConnectionSettingsStatus) bool {
	return oldStatus == nil || !bytes.Equal(oldStatus.LastConnectionSettingsHash, status.LastConnectionSettingsHash) ||
		oldStatus.Status == protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_APPLYING ||
		oldStatus.Status == protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_UNSET ||
		status.Status == protobufs.ConnectionSettingsStatuses_ConnectionSettingsStatuses_FAILED
}
