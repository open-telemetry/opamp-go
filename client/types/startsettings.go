package types

import (
	"crypto/tls"
	"net/http"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// StartSettings defines the parameters for starting the OpAMP Client.
type StartSettings struct {
	// Connection parameters.

	// Server URL. MUST be set.
	OpAMPServerURL string

	// Optional additional HTTP headers to send with all HTTP requests.
	Header http.Header

	// Optional TLS config for HTTP connection.
	TLSConfig *tls.Config

	// Agent information.
	InstanceUid string

	// Callbacks that the client will call after Start() returns nil.
	Callbacks Callbacks

	// Previously saved state. These will be reported to the Server immediately
	// after the connection is established.

	// The remote config status. If nil is passed it will force
	// the Server to send a remote config back.
	RemoteConfigStatus *protobufs.RemoteConfigStatus

	LastConnectionSettingsHash []byte

	// PackagesStateProvider provides access to the local state of packages.
	// If nil then ReportsPackageStatuses and AcceptsPackages capabilities will be disabled,
	// i.e. package status reporting and syncing from the Server will be disabled.
	PackagesStateProvider PackagesStateProvider

	// Defines the capabilities of the Agent. AgentCapabilities_ReportsStatus bit does not need to
	// be set in this field, it will be set automatically since it is required by OpAMP protocol.
	Capabilities protobufs.AgentCapabilities

	// Defines the custom capabilities of the Agent. Each capability is a reverse FQDN with
	// optional version information that uniquely identifies the custom capability and
	// should match a capability specified in a supported CustomMessage. The client will
	// automatically ignore any CustomMessage that contains a custom capability that is not
	// specified in this field.
	//
	// See
	// https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#customcapabilities
	// for more details.
	CustomCapabilities []string

	// EnableCompression can be set to true to enable the compression. Note that for WebSocket transport
	// the compression is only effectively enabled if the Server also supports compression.
	// The data will be compressed in both directions.
	EnableCompression bool
}
