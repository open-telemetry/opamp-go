package types

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

// StartSettings defines the parameters for starting the OpAMP Client.
type StartSettings struct {
	// Connection parameters.

	// Server URL. MUST be set.
	OpAMPServerURL string

	// Optional additional HTTP headers to send with all HTTP requests.
	Header http.Header

	// Optional HTTP client used by the plain HTTP transport for OpAMP requests.
	// If nil, a default HTTP client will be used. WebSocket transport ignores this field.
	Client *http.Client

	// Optional function that can be used to modify the HTTP headers
	// before each HTTP request.
	// Can modify and return the argument or return the argument without modifying.
	HeaderFunc func(http.Header) http.Header

	// Optional TLS config for HTTP connection.
	TLSConfig *tls.Config

	// Optional Proxy configuration
	// The ProxyURL may be http(s) or socks5; if no schema is specified http is assumed.
	ProxyURL string
	// ProxyHeaders gives the headers an HTTP client will present on a proxy CONNECT request.
	ProxyHeaders http.Header

	// Agent information.
	InstanceUid InstanceUid

	// Callbacks that the client will call after Start() returns nil.
	Callbacks Callbacks

	// Previously saved state. These will be reported to the Server immediately
	// after the connection is established.

	// The remote config status. If nil is passed it will force
	// the Server to send a remote config back.
	RemoteConfigStatus *protobufs.RemoteConfigStatus

	// The last offered connection settings status.
	LastConnectionSettingsStatus *protobufs.ConnectionSettingsStatus

	// PackagesStateProvider provides access to the local state of packages.
	// If nil then ReportsPackageStatuses and AcceptsPackages capabilities will be disabled,
	// i.e. package status reporting and syncing from the Server will be disabled.
	PackagesStateProvider PackagesStateProvider

	// PayloadVerifier validates the X.509 trust chain delivered in the
	// initial SignedServerToAgent.trust_chain_response of a connection
	// and verifies the detached signature on every subsequent
	// ServerToAgent message. MUST be set when the Agent's capability
	// set includes
	// AgentCapabilities_RequiresPayloadTrustVerification. When nil
	// (the default), payload trust verification is disabled and the
	// Server-to-Agent wire format is the standard ServerToAgent
	// protobuf.
	//
	// See the signing package for the in-process LocalVerifier
	// implementation and the VerifierFromFile helper that constructs
	// one from a PEM-encoded CA bundle.
	PayloadVerifier signing.Verifier

	// PayloadTOFUStore enables Trust On First Use (TOFU) enrollment for the
	// payload trust anchor. Mutually exclusive with PayloadVerifier: if
	// PayloadVerifier is also set it takes precedence and PayloadTOFUStore
	// is ignored.
	//
	// On startup the client calls PayloadTOFUStore.Load():
	//   - If a trust anchor is returned, it is used as PayloadVerifier for
	//     this session (normal attestation path).
	//   - If no anchor is stored yet, the client advertises
	//     AgentCapabilities_AcceptsPayloadTrustAnchorTOFU alongside
	//     AgentCapabilities_RequiresPayloadTrustVerification, accepts the
	//     root CA from the first TrustChainResponse.tofu_trust_anchor, and
	//     persists it via PayloadTOFUStore.Save().
	//
	// WARNING: TOFU provides no security on the first connection; a
	// compromised distribution server can install an attacker-controlled
	// trust anchor. Disable by default and enable only for environments
	// where the first connection is considered sufficiently trusted.
	// Requires persistent storage across restarts — agents running in
	// stateless container environments without a persistent volume will
	// repeat TOFU enrollment on every restart.
	PayloadTOFUStore signing.TOFUStore

	// Defines the capabilities of the Agent. AgentCapabilities_ReportsStatus bit does not need to
	// be set in this field, it will be set automatically since it is required by OpAMP protocol.
	// Deprecated: Use client.SetCapabilities() instead.
	Capabilities protobufs.AgentCapabilities

	// EnableCompression can be set to true to enable the compression. Note that for WebSocket transport
	// the compression is only effectively enabled if the Server also supports compression.
	// The data will be compressed in both directions.
	EnableCompression bool

	// MaxMessageSize is the maximum size in bytes of OpAMP transport messages
	// that the client sends or receives. For HTTP this applies to the complete
	// request or response body before compression and after decompression. For
	// WebSocket this applies to the complete OpAMP WebSocket message, including
	// header and data, before compression and after decompression.
	// If zero, the default limit of 64 MiB is used. If negative, no limit is applied.
	MaxMessageSize int64

	// Optional HeartbeatInterval to configure the heartbeat interval for client.
	// If nil, the default heartbeat interval (30s) will be used.
	// If zero, heartbeat will be disabled for a Websocket-based client.
	//
	// Note that an HTTP-based client will use the heartbeat interval as its polling interval
	// and zero is invalid for an HTTP-based client.
	//
	// If the ReportsHeartbeat capability is disabled, this option has no effect.
	HeartbeatInterval *time.Duration

	// Optional DownloadReporterInterval to configure how often a client reports the status of a package that is being downloaded.
	// If nil, the default reporter interval (10s) will be used.
	// If specified a minimum value of 1s will be enforced.
	DownloadReporterInterval *time.Duration
}
