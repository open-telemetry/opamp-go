package client

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/protobufs"
)

type StartSettings struct {
	// Connection parameters.
	OpAMPServerURL      string
	AuthorizationHeader string
	TLSConfig           *tls.Config

	// Agent information.
	InstanceUid  string
	AgentType    string
	AgentVersion string
	StartTime    time.Time

	// Callbacks that the client will call after Start() returns nil.
	Callbacks types.Callbacks

	// Previously saved state. This will be reported to the server immediately
	// after the connection is established.

	// The hash of the last received remote config. If nil is passed it will force
	// the server to send a remote config back.
	LastRemoteConfigHash []byte

	// The last saved effective config. If nil is passed the server will
	// keep the last reported effective config of this agent from previous
	// client connection sessions, if any. If there were no previous connection
	// sessions the effective config at the server will remain unknown.
	LastEffectiveConfig *protobufs.EffectiveConfig

	LastConnectionSettingsHash []byte

	// The hash of the last locally-saved server-provided addons. If nil is passed
	// it will force the server to send addons list back.
	LastServerProvidedAllAddonsHash []byte
}

type OpAMPClient interface {

	// Start the client and begin attempts to connect to the server. Once connection
	// is established the client will attempt to maintain it by reconnecting if
	// the connection is lost. All failed connection attempts will be reported via
	// OnConnectFailed callback.
	//
	// AgentType and AgentVersion will be included in all status reports sent to server.
	// If the type or the version have changed, Shutdown and Start the client again.
	//
	// Start may immediately return an error if the settings are incorrect (e.g. the
	// serverURL is not a valid URL).
	//
	// Start does not wait until the connection to the server is established and will
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
	Start(settings StartSettings) error

	// Stop the client. May be called only after Start() returns successfully.
	// May be called only once.
	// After this call returns successfully it is guaranteed that no
	// callbacks will be called. Stop() will cancel context of any in-fly
	// callbacks, but will wait until such in-fly callbacks are returned before
	// Stop returns, so make sure the callbacks don't block infinitely and react
	// promptly to context cancellations.
	// Once stopped OpAMPClient cannot be started again.
	Stop(ctx context.Context) error

	// SetAgentAttributes sets attributes of the Agent. The attributes will be included
	// in the next status report sent to the server.
	// MAY be called before Start(), in which case the attributes will be
	// included in the first start report after  connection is established.
	// MAY also be called after Start(), in which case the attributes will be included
	// in the next outgoing status report.
	SetAgentAttributes(attrs map[string]protobufs.AnyValue) error

	// SetEffectiveConfig sets the effective config of the Agent. The config will be
	// included in the next status report sent to the server. The behavior regarding
	// Start() method is exactly the same as for SetAgentAttributes() method.
	// Note that this is one of the 3 ways the effective config can be updated on the
	// server. The other 2 ways are:
	//   1) via StartSettings before Start()
	//   2) by returning an effective config in OnRemoteConfig callback.
	SetEffectiveConfig(config *protobufs.EffectiveConfig) error
}
