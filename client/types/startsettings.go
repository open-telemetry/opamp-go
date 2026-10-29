package types

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// BackoffPolicy controls the delay between consecutive connection or request
// retry attempts. The client calls NextBackOff to determine how long to wait
// before the next one.
type BackoffPolicy interface {
	// NextBackOff returns the duration to wait before the next retry.
	NextBackOff() time.Duration
}

// BackoffPolicyFunc returns a fresh BackoffPolicy. The client invokes it at
// the start of each retry sequence.
type BackoffPolicyFunc func() BackoffPolicy

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

	// DialContext, if set, overrides how the client establishes the underlying
	// network connection. The network and addr arguments passed to it are
	// derived from OpAMPServerURL but may be ignored by the implementation,
	// e.g. to dial a filesystem-path Unix domain socket regardless of the
	// (cosmetic) OpAMPServerURL host:
	//
	//   DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
	//       return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	//   }
	//
	// In that case OpAMPServerURL still supplies the scheme, path, and Host
	// header (e.g. "ws://localhost/v1/opamp" or "http://localhost/v1/opamp").
	// For the HTTP transport, Client.Transport must be an *http.Transport (or
	// nil) so the dialer can be applied; Start() returns an error otherwise.
	// Setting both DialContext and ProxyURL is not supported; Start() returns
	// an error if both are set.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

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

	// Optional BackoffPolicy returns a fresh policy controlling the delay between
	// consecutive retry attempts when a connection (WebSocket) or request
	// (HTTP) fails. It is invoked at the start of each retry sequence, so every
	// sequence begins from the returned policy's initial state. If nil, a
	// default exponential backoff is used that retries indefinitely.
	// See BackoffPolicy and BackoffPolicyFunc for details.
	BackoffPolicy BackoffPolicyFunc
}
