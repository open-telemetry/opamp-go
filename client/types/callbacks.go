package types

import (
	"context"

	"github.com/open-telemetry/opamp-go/protobufs"
)

type OwnTelemetryType int

const (
	OwnMetrics OwnTelemetryType = iota
	OwnTraces
	OwnLogs
)

type Callbacks interface {
	OnConnect()

	// OnConnectFailed is called when the connection to the server cannot be established.
	// May be called after Start() is called and tries to connect to the server.
	// May also be called if the connection is lost and reconnection attempt fails.
	OnConnectFailed(err error)

	// OnError is called when the server reports an error in response to some previously
	// sent request. Useful for logging purposes. The Agent should not attempt to process
	// the error by reconnecting or retrying previous operations. The client handles the
	// ErrorResponse_UNAVAILABLE case internally by performing retries as necessary.
	OnError(err *protobufs.ServerErrorResponse)

	// OnRemoteConfig is called when the agent receives a remote config from the server.
	// Note that the config parameter may be nil, which indicates that the remote
	// is the same as what the Agent reported it has via the last_remote_config_hash
	// field of the RemoteConfigStatus message last time.
	//
	// The Agent should process the config and return the effective config if processing
	// succeeded or an error if processing failed.
	//
	// configChanged must be set to true if as a result of applying the remote config
	// the effective config has changed.
	//
	// The returned effective config or the error will be reported back to the server
	// via StatusReport message (using EffectiveConfig and RemoteConfigStatus fields).
	//
	// Only one OnRemoteConfig call can be active at any time. Until OnRemoteConfig
	// returns it will not be called again. Any other remote configs received from
	// the server while OnRemoteConfig call has not returned will be remembered.
	// Once OnRemoteConfig call returns it will be called again with the most recent
	// remote config received.
	OnRemoteConfig(
		ctx context.Context,
		remoteConfig *protobufs.AgentRemoteConfig,
	) (effectiveConfig *protobufs.EffectiveConfig, configChanged bool, err error)

	// OnOpampConnectionSettings is called when the agent receives an OpAMP
	// connection settings offer from the server. Typically the settings can specify
	// authorization headers or TLS certificate, potentially also a different
	// OpAMP destination to work with.
	//
	// The Agent should process the offer and return an error if the Agent does not
	// want to accept the settings (e.g. if the TSL certificate in the settings
	// cannot be verified). The returned error will be reported back to the server
	// via StatusReport message (using ConnectionStatuses field).
	//
	// If OnOpampConnectionSettings returns nil and then the caller will
	// attempt to reconnect to the OpAMP server using the new settings.
	// If the connection fails the settings will be rejected and an error will
	// be reported to the server. If the connection succeeds the new settings
	// will be used by the client from that moment on.
	//
	// Accepted or rejected settings will be reported back to the server via
	// ConnectionStatuses message.
	//
	// Only one OnOpampConnectionSettings call can be active at any time.
	// See OnRemoteConfig for the behavior.
	OnOpampConnectionSettings(
		ctx context.Context,
		settings *protobufs.ConnectionSettings,
	) error

	// OnOpampConnectionSettingsAccepted will be called after the settings are
	// verified and accepted (OnOpampConnectionSettingsOffer and connection using
	// new settings succeeds). The agent should store the settings and use them
	// in the future. Old connection settings should be forgotten.
	OnOpampConnectionSettingsAccepted(
		settings *protobufs.ConnectionSettings,
	)

	// OnOwnTelemetryConnectionSettings is called when the agent receives a
	// connection settings to be used for reporting agent's own telemetry.
	//
	// The Agent should process the settings and return an error if the Agent does not
	// want to accept the settings (e.g. if the TSL certificate in the settings
	// cannot be verified). The returned error will be reported back to the server
	// via StatusReport message (using ConnectionStatuses field).
	// If the Agent accepts the settings it should return nil and begin sending
	// its own telemetry to the destination specified in the settings.
	// We currently support 3 types of agent's own telemetry: metrics, traces, logs.
	// The agent can support any subset of these types.
	OnOwnTelemetryConnectionSettings(
		ctx context.Context,
		telemetryType OwnTelemetryType,
		settings *protobufs.ConnectionSettings,
	) error

	// OnOtherConnectionSettings is called when the agent receives a
	// connection settings to be used for by the agent for any other purposes.
	// Typically these are used by the agent to send collected data to the destinations
	// it is configured to. The name is typically the name of the destination.
	//
	// The Agent should process the settings and return an error if the Agent does not
	// want to accept the settings (e.g. if the TSL certificate in the settings
	// cannot be verified). The returned error will be reported back to the server
	// via StatusReport message (using ConnectionStatuses field).
	OnOtherConnectionSettings(
		ctx context.Context,
		name string,
		certificate *protobufs.ConnectionSettings,
	) error

	// OnAddonsAvailable is called when the server has addons available which are
	// different from what the agent indicated it has via
	// LastServerProvidedAllAddonsHash.
	// syncer can be used to initiate syncing the addons from the server.
	OnAddonsAvailable(ctx context.Context, addons *protobufs.AddonsAvailable, syncer AddonSyncer) error

	// OnAgentPackageAvailable is called when the server has an agent package available
	// for the agent.
	// syncer can be used to initiate syncing the package from the server.
	OnAgentPackageAvailable(addons *protobufs.AgentPackageAvailable, syncer AgentPackageSyncer) error

	// For all methods that accept a context parameter the caller may cancel the
	// context if processing takes too long. In that case the method should return
	// as soon as possible with an error.
}
