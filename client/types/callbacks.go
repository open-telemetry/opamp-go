package types

import (
	"context"

	"github.com/open-telemetry/opamp-go/protobufs"
)

type MessageData struct {
	// RemoteConfig is offered by the Server. The Agent must process it and call
	// OpAMPClient.SetRemoteConfigStatus to indicate success or failure. If the
	// effective config has changed as a result of processing the Agent must also call
	// OpAMPClient.UpdateEffectiveConfig. SetRemoteConfigStatus and UpdateEffectiveConfig
	// may be called from OnMessage handler or after OnMessage returns.
	RemoteConfig *protobufs.AgentRemoteConfig

	// Connection settings are offered by the Server. These fields should be processed
	// as described in the ConnectionSettingsOffers message.
	OwnMetricsConnSettings *protobufs.TelemetryConnectionSettings
	OwnTracesConnSettings  *protobufs.TelemetryConnectionSettings
	OwnLogsConnSettings    *protobufs.TelemetryConnectionSettings
	OtherConnSettings      map[string]*protobufs.OtherConnectionSettings

	// PackagesAvailable offered by the Server. The Agent must process the offer.
	// The typical way to process is to call PackageSyncer.Sync() function, which will
	// take care of reporting the status to the Server as processing happens.
	//
	// If PackageSyncer.Sync() function is not called then it is the responsibility of
	// OnMessage handler to do the processing and call OpAMPClient.SetPackageStatuses to
	// reflect the processing status. SetPackageStatuses may be called from OnMessage
	// handler or after OnMessage returns.
	PackagesAvailable *protobufs.PackagesAvailable
	PackageSyncer     PackagesSyncer

	// AgentIdentification indicates a new identification received from the Server.
	// The Agent must save this identification and use it in the future instantiations
	// of OpAMPClient.
	AgentIdentification *protobufs.AgentIdentification
}

type Callbacks interface {
	OnConnect()

	// OnConnectFailed is called when the connection to the Server cannot be established.
	// May be called after Start() is called and tries to connect to the Server.
	// May also be called if the connection is lost and reconnection attempt fails.
	OnConnectFailed(err error)

	// OnError is called when the Server reports an error in response to some previously
	// sent request. Useful for logging purposes. The Agent should not attempt to process
	// the error by reconnecting or retrying previous operations. The client handles the
	// ErrorResponse_UNAVAILABLE case internally by performing retries as necessary.
	OnError(err *protobufs.ServerErrorResponse)

	// OnMessage is called when the Agent receives a message that needs processing.
	// See MessageData definition for the data that may be available for processing.
	// During OnMessage execution the OpAMPClient functions that change the status
	// of the client may be called, e.g. if RemoteConfig is processed then
	// SetRemoteConfigStatus should be called to reflect the processing result.
	// These functions may also be called after OnMessage returns. This is advisable
	// if processing can take a long time. In that case returning quickly is preferable
	// to avoid blocking the OpAMPClient.
	OnMessage(ctx context.Context, msg *MessageData)

	// OnOpampConnectionSettings is called when the Agent receives an OpAMP
	// connection settings offer from the Server. Typically the settings can specify
	// authorization headers or TLS certificate, potentially also a different
	// OpAMP destination to work with.
	//
	// The Agent should process the offer and return an error if the Agent does not
	// want to accept the settings (e.g. if the TSL certificate in the settings
	// cannot be verified).
	//
	// If OnOpampConnectionSettings returns nil and then the caller will
	// attempt to reconnect to the OpAMP Server using the new settings.
	// If the connection fails the settings will be rejected and an error will
	// be reported to the Server. If the connection succeeds the new settings
	// will be used by the client from that moment on.
	//
	// Only one OnOpampConnectionSettings call can be active at any time.
	// See OnRemoteConfig for the behavior.
	OnOpampConnectionSettings(
		ctx context.Context,
		settings *protobufs.OpAMPConnectionSettings,
	) error

	// OnOpampConnectionSettingsAccepted will be called after the settings are
	// verified and accepted (OnOpampConnectionSettingsOffer and connection using
	// new settings succeeds). The Agent should store the settings and use them
	// in the future. Old connection settings should be forgotten.
	OnOpampConnectionSettingsAccepted(
		settings *protobufs.OpAMPConnectionSettings,
	)

	// For all methods that accept a context parameter the caller may cancel the
	// context if processing takes too long. In that case the method should return
	// as soon as possible with an error.

	// SaveRemoteConfigStatus is called after OnRemoteConfig returns. The status
	// will be set either as APPLIED or FAILED depending on whether OnRemoteConfig
	// returned a success or error.
	// The Agent must remember this RemoteConfigStatus and supply in the future
	// calls to Start() in StartSettings.RemoteConfigStatus.
	SaveRemoteConfigStatus(ctx context.Context, status *protobufs.RemoteConfigStatus)

	// GetEffectiveConfig returns the current effective config. Only one
	// GetEffectiveConfig call can be active at any time. Until GetEffectiveConfig
	// returns it will not be called again.
	GetEffectiveConfig(ctx context.Context) (*protobufs.EffectiveConfig, error)

	// OnCommand is called when the Server requests that the connected Agent perform a command.
	OnCommand(command *protobufs.ServerToAgentCommand) error
}

type CallbacksStruct struct {
	OnConnectFunc       func()
	OnConnectFailedFunc func(err error)
	OnErrorFunc         func(err *protobufs.ServerErrorResponse)

	OnMessageFunc func(ctx context.Context, msg *MessageData)

	OnOpampConnectionSettingsFunc func(
		ctx context.Context,
		settings *protobufs.OpAMPConnectionSettings,
	) error
	OnOpampConnectionSettingsAcceptedFunc func(
		settings *protobufs.OpAMPConnectionSettings,
	)

	OnCommandFunc func(command *protobufs.ServerToAgentCommand) error

	SaveRemoteConfigStatusFunc func(ctx context.Context, status *protobufs.RemoteConfigStatus)
	GetEffectiveConfigFunc     func(ctx context.Context) (*protobufs.EffectiveConfig, error)
}

var _ Callbacks = (*CallbacksStruct)(nil)

func (c CallbacksStruct) OnConnect() {
	if c.OnConnectFunc != nil {
		c.OnConnectFunc()
	}
}

func (c CallbacksStruct) OnConnectFailed(err error) {
	if c.OnConnectFailedFunc != nil {
		c.OnConnectFailedFunc(err)
	}
}

func (c CallbacksStruct) OnError(err *protobufs.ServerErrorResponse) {
	if c.OnErrorFunc != nil {
		c.OnErrorFunc(err)
	}
}

func (c CallbacksStruct) OnMessage(ctx context.Context, msg *MessageData) {
	if c.OnMessageFunc != nil {
		c.OnMessageFunc(ctx, msg)
	}
}

func (c CallbacksStruct) SaveRemoteConfigStatus(ctx context.Context, status *protobufs.RemoteConfigStatus) {
	if c.SaveRemoteConfigStatusFunc != nil {
		c.SaveRemoteConfigStatusFunc(ctx, status)
	}
}

func (c CallbacksStruct) GetEffectiveConfig(ctx context.Context) (*protobufs.EffectiveConfig, error) {
	if c.GetEffectiveConfigFunc != nil {
		return c.GetEffectiveConfigFunc(ctx)
	}
	return nil, nil
}

func (c CallbacksStruct) OnOpampConnectionSettings(
	ctx context.Context, settings *protobufs.OpAMPConnectionSettings,
) error {
	if c.OnOpampConnectionSettingsFunc != nil {
		return c.OnOpampConnectionSettingsFunc(ctx, settings)
	}
	return nil
}

func (c CallbacksStruct) OnOpampConnectionSettingsAccepted(settings *protobufs.OpAMPConnectionSettings) {
	if c.OnOpampConnectionSettingsAcceptedFunc != nil {
		c.OnOpampConnectionSettingsAcceptedFunc(settings)
	}
}

func (c CallbacksStruct) OnCommand(command *protobufs.ServerToAgentCommand) error {
	if c.OnCommandFunc != nil {
		return c.OnCommandFunc(command)
	}
	return nil
}
