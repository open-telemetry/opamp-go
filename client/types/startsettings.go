package types

import (
	"crypto/tls"

	"github.com/open-telemetry/opamp-go/protobufs"
)

type StartSettings struct {
	// Connection parameters.

	// Server URL. MUST be set.
	OpAMPServerURL string

	// Optional value of "Authorization" HTTP header in the HTTP request.
	AuthorizationHeader string

	// Optional TLS config for HTTP connection.
	TLSConfig *tls.Config

	// Agent information.
	InstanceUid string

	// AgentDescription MUST be set and MUST describe the Agent. See OpAMP spec
	// for details.
	AgentDescription *protobufs.AgentDescription

	// Callbacks that the client will call after Start() returns nil.
	Callbacks Callbacks

	// Previously saved state. These will be reported to the server immediately
	// after the connection is established.

	// The remote config status. If nil is passed it will force
	// the server to send a remote config back. It is not required to set the Hash
	// field, it will be calculated by Start() function.
	RemoteConfigStatus *protobufs.RemoteConfigStatus

	LastConnectionSettingsHash []byte

	// The hash of the last locally-saved server-provided packages. If nil is passed
	// it will force the server to send packages list back.
	LastServerProvidedAllPackagesHash []byte
}
