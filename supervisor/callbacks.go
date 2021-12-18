package supervisor

import (
	"context"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var _ types.Callbacks = (*supervisor)(nil)

func (s *supervisor) OnConnect() {
	s.logger.Debugf("Connected to the server")
}

func (s *supervisor) OnConnectFailed(err error) {
	s.logger.Errorf("Failed to connect to the server: %v", err)
}

func (s *supervisor) OnError(err *protobufs.ServerErrorResponse) {
	s.logger.Errorf("Server returned an error response: %v", err.ErrorMessage)
}

func (s *supervisor) OnRemoteConfig(
	ctx context.Context, remoteConfig *protobufs.AgentRemoteConfig,
) (*protobufs.EffectiveConfig, error) {
	return s.configurer.UpdateConfig(
		ctx,
		s.config.AgentSettings.ConfigSpec,
		remoteConfig,
		s.watcher)
}

func (s *supervisor) OnAgentPackageAvailable(
	pkg *protobufs.AgentPackageAvailable, _ types.AgentPackageSyncer,
) error {
	return s.packager.Sync(
		context.Background(), // TODO: the context should be provided on the callback
		pkg,
		s.watcher,
		func(file string, percent float32) {
			s.logger.Debugf("Downloading %q (%f)", file, percent)
		},
	)
}

// !!!!!!!!!!!!!!!!!!!!
// !!! UNIMPLEMENTED!!!
// !!!!!!!!!!!!!!!!!!!!

func (s *supervisor) OnOpampConnectionSettings(
	ctx context.Context, settings *protobufs.ConnectionSettings,
) error {
	panic("unimplemented")
}

func (s *supervisor) OnOpampConnectionSettingsAccepted(settings *protobufs.ConnectionSettings) {
	panic("unimplemented")
}

func (s *supervisor) OnOwnTelemetryConnectionSettings(
	ctx context.Context, telemetryType types.OwnTelemetryType,
	settings *protobufs.ConnectionSettings,
) error {
	panic("unimplemented")
}

func (s *supervisor) OnOtherConnectionSettings(
	ctx context.Context, name string, settings *protobufs.ConnectionSettings,
) error {
	panic("unimplemented")
}

func (s *supervisor) OnAddonsAvailable(
	ctx context.Context,
	addons *protobufs.AddonsAvailable,
	syncer types.AddonSyncer,
) error {
	panic("unimplemented")
}
