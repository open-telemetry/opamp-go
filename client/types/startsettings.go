package types

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
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

	// RetryStatusCodes overrides the set of HTTP response status codes that the
	// plain-HTTP transport treats as retryable. When a response carries one of these
	// codes the client retries the request with exponential backoff (honoring any
	// Retry-After header) and invokes OnConnectFailed for each failed attempt.
	//
	// If nil, the library default of []int{http.StatusTooManyRequests,
	// http.StatusServiceUnavailable} is used. A non-nil value (including an empty
	// slice) fully replaces the default, so callers that want to keep 429/503
	// retryable while adding additional codes must include them explicitly, e.g.
	// []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusUnauthorized}.
	//
	// Only applies to the plain-HTTP transport; the WebSocket transport ignores
	// this field.
	RetryStatusCodes []int
}
