package client

import (
	"context"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// OpAMPClient is an interface representing the client side of the OpAMP protocol.
type OpAMPClient interface {

	// Start the client and begin attempts to connect to the Server. Once connection
	// is established the client will attempt to maintain it by reconnecting if
	// the connection is lost. All failed connection attempts will be reported via
	// OnConnectFailed callback.
	//
	// SetAgentDescription() MUST be called before Start().
	//
	// Start may immediately return an error if the settings are incorrect (e.g. the
	// serverURL is not a valid URL).
	//
	// Start does not wait until the connection to the Server is established and will
	// likely return before the connection attempts are even made.
	//
	// It is guaranteed that after the Start() call returns without error one of the
	// following callbacks will be called eventually (unless Stop() is called earlier):
	//  - OnConnectFailed
	//  - OnError
	//  - OnRemoteConfig
	//
	// Start should be called only once. It should not be called concurrently with
	// any other OpAMPClient methods.
	Start(ctx context.Context, settings types.StartSettings) error

	// Stop the client. May be called only after Start() returns successfully.
	// May be called only once.
	// After this call returns successfully it is guaranteed that no
	// callbacks will be called. Stop() will cancel context of any in-fly
	// callbacks.
	//
	// If a callback is in progress (e.g. OnMessage is called but not finished)
	// Stop() initiates stopping and returns without waiting for stopping to finish.
	//
	// Once stopped OpAMPClient cannot be started again.
	Stop(ctx context.Context) error

	// SetAgentDescription sets attributes of the Agent. The attributes will be included
	// in the next status report sent to the Server. MUST be called before Start().
	// May be also called after Start(), in which case the attributes will be included
	// in the next outgoing status report. This is typically used by Agents which allow
	// their AgentDescription to change dynamically while the OpAMPClient is started.
	// May be also called from OnMessage handler.
	//
	// nil values are not allowed and will return an error.
	SetAgentDescription(descr *protobufs.AgentDescription) error

	// AgentDescription returns the last value successfully set by SetAgentDescription().
	AgentDescription() *protobufs.AgentDescription

	// SetHealth sets the health status of the Agent. The AgentHealth will be included
	// in the next status report sent to the Server. MAY be called before or after Start().
	// May be also called after Start().
	// May be also called from OnMessage handler.
	//
	// nil health parameter is not allowed and will return an error.
	SetHealth(health *protobufs.AgentHealth) error

	// UpdateEffectiveConfig fetches the current local effective config using
	// GetEffectiveConfig callback and sends it to the Server.
	// May be called anytime after Start(), including from OnMessage handler.
	UpdateEffectiveConfig(ctx context.Context) error

	// SetRemoteConfigStatus sets the current RemoteConfigStatus.
	// LastRemoteConfigHash field must be non-nil.
	// May be called anytime after Start(), including from OnMessage handler.
	// nil values are not allowed and will return an error.
	SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error

	// SetPackageStatuses sets the current PackageStatuses.
	// ServerProvidedAllPackagesHash must be non-nil.
	// May be called anytime after Start(), including from OnMessage handler.
	// nil values are not allowed and will return an error.
	SetPackageStatuses(statuses *protobufs.PackageStatuses) error
}
