package types

import (
	"context"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// MessageData represents a message received from the server and handled by Callbacks.
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

// Callbacks is an interface for the Client to handle messages from the Server.
// Callbacks are expected to honour the context passed to them, meaning they should be aware of cancellations.
type Callbacks interface {
	// OnConnect is called when the connection is successfully established to the Server.
	// May be called after Start() is called and every time a connection is established to the Server.
	// For WebSocket clients this is called after the handshake is completed without any error.
	// For HTTP clients this is called for any request if the response status is OK.
	OnConnect(ctx context.Context)

	// OnConnectFailed is called when the connection to the Server cannot be established.
	// May be called after Start() is called and tries to connect to the Server.
	// May also be called if the connection is lost and reconnection attempt fails.
	OnConnectFailed(ctx context.Context, err error)

	// OnError is called when the Server reports an error in response to some previously
	// sent request. Useful for logging purposes. The Agent should not attempt to process
	// the error by reconnecting or retrying previous operations. The client handles the
	// ErrorResponse_UNAVAILABLE case internally by performing retries as necessary.
	OnError(ctx context.Context, err *protobufs.ServerErrorResponse)

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
	// connection settings offer from the Server. Typically, the settings can specify
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
	OnOpampConnectionSettingsAccepted(ctx context.Context, settings *protobufs.OpAMPConnectionSettings)

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
	OnCommand(ctx context.Context, command *protobufs.ServerToAgentCommand) error
}

// CallbacksStruct is a struct that implements Callbacks interface and allows
// to override only the methods that are needed. If a method is not overridden then it is a no-op.
type CallbacksStruct struct {
	OnConnectFunc       func(ctx context.Context)
	OnConnectFailedFunc func(ctx context.Context, err error)
	OnErrorFunc         func(ctx context.Context, err *protobufs.ServerErrorResponse)

	OnMessageFunc func(ctx context.Context, msg *MessageData)

	OnOpampConnectionSettingsFunc func(
		ctx context.Context,
		settings *protobufs.OpAMPConnectionSettings,
	) error
	OnOpampConnectionSettingsAcceptedFunc func(
		ctx context.Context,
		settings *protobufs.OpAMPConnectionSettings,
	)

	OnCommandFunc func(ctx context.Context, command *protobufs.ServerToAgentCommand) error

	SaveRemoteConfigStatusFunc func(ctx context.Context, status *protobufs.RemoteConfigStatus)
	GetEffectiveConfigFunc     func(ctx context.Context) (*protobufs.EffectiveConfig, error)
}

var _ Callbacks = (*CallbacksStruct)(nil)

// OnConnect implements Callbacks.OnConnect.
func (c CallbacksStruct) OnConnect(ctx context.Context) {
	if c.OnConnectFunc != nil {
		c.OnConnectFunc(ctx)
	}
}

// OnConnectFailed implements Callbacks.OnConnectFailed.
func (c CallbacksStruct) OnConnectFailed(ctx context.Context, err error) {
	if c.OnConnectFailedFunc != nil {
		c.OnConnectFailedFunc(ctx, err)
	}
}

// OnError implements Callbacks.OnError.
func (c CallbacksStruct) OnError(ctx context.Context, err *protobufs.ServerErrorResponse) {
	if c.OnErrorFunc != nil {
		c.OnErrorFunc(ctx, err)
	}
}

// OnMessage implements Callbacks.OnMessage.
func (c CallbacksStruct) OnMessage(ctx context.Context, msg *MessageData) {
	if c.OnMessageFunc != nil {
		c.OnMessageFunc(ctx, msg)
	}
}

// SaveRemoteConfigStatus implements Callbacks.SaveRemoteConfigStatus.
func (c CallbacksStruct) SaveRemoteConfigStatus(ctx context.Context, status *protobufs.RemoteConfigStatus) {
	if c.SaveRemoteConfigStatusFunc != nil {
		c.SaveRemoteConfigStatusFunc(ctx, status)
	}
}

// GetEffectiveConfig implements Callbacks.GetEffectiveConfig.
func (c CallbacksStruct) GetEffectiveConfig(ctx context.Context) (*protobufs.EffectiveConfig, error) {
	if c.GetEffectiveConfigFunc != nil {
		return c.GetEffectiveConfigFunc(ctx)
	}
	return nil, nil
}

// OnOpampConnectionSettings implements Callbacks.OnOpampConnectionSettings.
func (c CallbacksStruct) OnOpampConnectionSettings(
	ctx context.Context, settings *protobufs.OpAMPConnectionSettings,
) error {
	if c.OnOpampConnectionSettingsFunc != nil {
		return c.OnOpampConnectionSettingsFunc(ctx, settings)
	}
	return nil
}

// OnOpampConnectionSettingsAccepted implements Callbacks.OnOpampConnectionSettingsAccepted.
func (c CallbacksStruct) OnOpampConnectionSettingsAccepted(ctx context.Context, settings *protobufs.OpAMPConnectionSettings) {
	if c.OnOpampConnectionSettingsAcceptedFunc != nil {
		c.OnOpampConnectionSettingsAcceptedFunc(ctx, settings)
	}
}

// OnCommand implements Callbacks.OnCommand.
func (c CallbacksStruct) OnCommand(ctx context.Context, command *protobufs.ServerToAgentCommand) error {
	if c.OnCommandFunc != nil {
		return c.OnCommandFunc(ctx, command)
	}
	return nil
}
