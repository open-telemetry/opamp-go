package client

import (
	"context"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

type CallbacksStruct struct {
	OnConnectFunc       func()
	OnConnectFailedFunc func(err error)
	OnErrorFunc         func(err *protobufs.ServerErrorResponse)

	OnRemoteConfigFunc func(
		ctx context.Context,
		config *protobufs.AgentRemoteConfig,
	) (*protobufs.EffectiveConfig, error)

	OnOpampConnectionSettingsFunc func(
		ctx context.Context,
		settings *protobufs.ConnectionSettings,
	) error
	OnOpampConnectionSettingsAcceptedFunc func(
		settings *protobufs.ConnectionSettings,
	)

	OnOwnTelemetryConnectionSettingsFunc func(
		ctx context.Context,
		telemetryType types.OwnTelemetryType,
		settings *protobufs.ConnectionSettings,
	) error

	OnOtherConnectionSettingsFunc func(
		ctx context.Context,
		name string,
		settings *protobufs.ConnectionSettings,
	) error

	OnAddonsAvailableFunc       func(ctx context.Context, addons *protobufs.AddonsAvailable, syncer types.AddonSyncer) error
	OnAgentPackageAvailableFunc func(addons *protobufs.AgentPackageAvailable, syncer types.AgentPackageSyncer) error
}

var _ types.Callbacks = (*CallbacksStruct)(nil)

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

func (c CallbacksStruct) OnRemoteConfig(
	ctx context.Context, remoteConfig *protobufs.AgentRemoteConfig,
) (*protobufs.EffectiveConfig, error) {
	if c.OnRemoteConfigFunc != nil {
		return c.OnRemoteConfigFunc(ctx, remoteConfig)
	}
	return nil, nil
}

func (c CallbacksStruct) OnOpampConnectionSettings(
	ctx context.Context, settings *protobufs.ConnectionSettings,
) error {
	if c.OnOpampConnectionSettingsFunc != nil {
		return c.OnOpampConnectionSettingsFunc(ctx, settings)
	}
	return nil
}

func (c CallbacksStruct) OnOpampConnectionSettingsAccepted(settings *protobufs.ConnectionSettings) {
	if c.OnOpampConnectionSettingsAcceptedFunc != nil {
		c.OnOpampConnectionSettingsAcceptedFunc(settings)
	}
}

func (c CallbacksStruct) OnOwnTelemetryConnectionSettings(
	ctx context.Context, telemetryType types.OwnTelemetryType,
	settings *protobufs.ConnectionSettings,
) error {
	if c.OnOwnTelemetryConnectionSettingsFunc != nil {
		return c.OnOwnTelemetryConnectionSettingsFunc(ctx, telemetryType, settings)
	}
	return nil
}

func (c CallbacksStruct) OnOtherConnectionSettings(
	ctx context.Context, name string, settings *protobufs.ConnectionSettings,
) error {
	if c.OnOtherConnectionSettingsFunc != nil {
		return c.OnOtherConnectionSettingsFunc(ctx, name, settings)
	}
	return nil
}

func (c CallbacksStruct) OnAddonsAvailable(
	ctx context.Context,
	addons *protobufs.AddonsAvailable,
	syncer types.AddonSyncer,
) error {
	if c.OnAddonsAvailableFunc != nil {
		return c.OnAddonsAvailableFunc(ctx, addons, syncer)
	}
	return nil
}

func (c CallbacksStruct) OnAgentPackageAvailable(
	addons *protobufs.AgentPackageAvailable, syncer types.AgentPackageSyncer,
) error {
	if c.OnAgentPackageAvailableFunc != nil {
		return c.OnAgentPackageAvailableFunc(addons, syncer)
	}
	return nil
}
