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

	// HTTP2Config optionally configures HTTP/2 keepalive and per-request
	// response-header timeout for the HTTP transport. If nil, behavior is
	// unchanged (the Go default: no PING, no ResponseHeaderTimeout, which
	// means a half-dead HTTP/2 connection only surfaces via the kernel's
	// ~11-minute TCP keepalive ladder). Only consulted for the HTTP
	// transport; ignored for WebSocket.
	HTTP2Config *HTTP2ClientConfig

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

// HTTP2ClientConfig configures HTTP/2 keepalive behavior and a per-request
// response-header timeout on the HTTP transport used by the OpAMP HTTP client.
//
// All fields are zero-default: leaving any field as zero leaves that mechanism
// disabled. The common case is to populate SendPingTimeout + PingTimeout for
// proactive half-dead-connection detection on HTTP/2 connections, and
// optionally ResponseHeaderTimeout as a backstop for scenarios where HTTP/2
// PING cannot fire (HTTP/1.1 fallback, intermediaries that drop PING frames,
// servers that do not send PONG).
type HTTP2ClientConfig struct {
	// SendPingTimeout is the idle time after which the HTTP/2 transport will
	// send a PING frame on an otherwise-silent connection. Any inbound frame
	// resets the timer, so healthy long-poll traffic is not disrupted. Maps
	// to net/http.HTTP2Config.SendPingTimeout (Go 1.24+). If zero, no PINGs
	// are sent. A typical value is 30s.
	SendPingTimeout time.Duration

	// PingTimeout is the amount of time the transport will wait for a PONG
	// after sending a PING before closing the connection. Maps to
	// net/http.HTTP2Config.PingTimeout. If zero, PING acks are not enforced.
	// A typical value is 10s.
	PingTimeout time.Duration

	// ResponseHeaderTimeout bounds how long an outgoing request will wait
	// for the server's response headers. Maps to
	// net/http.Transport.ResponseHeaderTimeout. If zero, no per-request cap
	// is applied and a half-dead connection is only surfaced via TCP
	// keepalive (~11 minutes with Linux defaults) or HTTP/2 PING (if set).
	// Should be larger than the server's long-poll window and, when combined
	// with PING, larger than SendPingTimeout+PingTimeout so PING gets the
	// first chance at detection.
	ResponseHeaderTimeout time.Duration
}
