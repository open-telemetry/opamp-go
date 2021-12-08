package types

import (
	"crypto/tls"
	"time"
)

// Systemd represents this agent's systemd configuration that is used to
// restart this agent on configuration changes, executable upgrades, etc.
// At the minimum, the agent's systemd unit name must be specified so that
// the supervisor can issue commands like - `systemctl restart otelcol`.
// This configuration MUST NOT be specified along with the `Watchdog'
// configuration as they are mutually exclusive.
type Systemd struct {
	Name string
}

// WatchdogPolicy is the policy associated with the lifecycle management
// of the agent.
type WatchdogPolicy struct {
	// Restart indicates the conditions under which the agent must be restarted.
	// A value of 'always' indicates that the supervisor will always attempt
	// to restart the agent if it is not running. A value of 'failure' indicates
	// that the supervisor will restart only if the agent went down as a result
	// of a failure (non-zero exit code, for example).
	Restart string
	// RestartWait is the time to wait before restarting the agent (as configured
	// with `restart`).
	RestartWait time.Duration
	// MaxAttempts is the maximum number of attempts the agent is allowed to
	// fail a START, STOP or RESTART operation.
	MaxAttempts int
	// WaitBetweenAttempts is the duration to wait between successive attempts
	// to START, STOP or RESTART the agent.
	WaitBetweenAttempts time.Duration
}

// HealthCheck is the configuration that allows the supervisor to check the
// health status of the agent.
type HealthCheck struct {
	// Url is the url at which the agent reports its health status.
	Url          string
	// InitialDelay is the delay that must be observed before the first request
	// can be issued.
	InitialDelay time.Duration
	// Period is the interval at which the health status must be probed.
	Period       time.Duration
	// Timeout is the duration at which the supervisor concludes that the agent
	// is no longer healthy (due to successive failures in reaching the specified
	// URL).
	Timeout      time.Duration
}

// OwnTelemetry is the configuration that allows the supervisor to scrape the
// agent for its own telemetry.
type OwnTelemetry struct {
	// The URL where metrics can be scraped.
	Url          string
	// The initial delay that must be observed before the first request can
	// be issued.
	InitialDelay time.Duration
	// The interval at which the metrics should be scraped.
	Period       time.Duration
}

// Management represents a set of endpoints (currently, http) that are uesd
// to perform health-check on the agent, obtain the agent's own telemetry,
// etc.
type Management struct {
	// HealthCheck defines the specification for doing periodic health-checks
	// on the agent. OpenTelemetry, for example, by default reports the status
	// on the `:13133` port.
	HealthCheck  *HealthCheck
	// OwnTelemetry defines the specification for scraping agent's own telemetry
	// to report to a OTLP backend. In OpenTelemetry collector, the `:8888`
	// port, by default, allows scraping for prometheus.
	OwnTelemetry *OwnTelemetry
}

// Watchdog allows the supervisor to monitor the health of the agent in
// the absence of a `Systemd' installation of the agent. It also enables
// the supervisor to restart the agent during configuration changes and
// upgrades.
type Watchdog struct {
	// Args are the additional command-line arguments supplied to the executable.
	Args []string
	// Env is any additional environment variables that must be passed to the
	// agent process.
	Env map[string]string
	// Policy is this agent's watchdog policy.
	Policy *WatchdogPolicy
}

// AgentConfig represents this agent's configuration details: the location of
// the configuration file, for example, that must be updated on a remote
// configuration change.
type AgentConfig struct {
	// File is the path to the configuration file corresponding to the agent.
	File string
	// AutoReload, if true, indicates that the agent does not require a restart
	// on configuration change.
	AutoReload bool
}

// Agent specifies the agent managed by this supervisor. Concretely, it dictates
// where to find the agent, how to start it and its configuration file. This
// basic information is necessary for the supervisor to restart the agent, for
// example, on a remote configuration change.
type Agent struct {
	// Type represents the type of this agent. For example, `otelcol' can be
	// a valid agent type. Internally, the `type' can be used to construct
	// a purpose-built supervisor rather than a generic one. For example, a
	// `otelcol' agent type can construct a supervisor that can manage an
	// OpenTelemetry collector.
	Type string
	// Executable is the path to the agent's executable. For example, `/otel'
	// or `/fluent-bit/bin/fluent-bit'
	Executable string
	// Attrs are an arbitrary collection of key-value pairs that is specific
	// for this agent. An OTEL-collector, for example, can specify the
	// `service.name' and `service.namespace' as attributes that can be used
	// to identify this collector with the OpAMP server.
	Attrs map[string]string
	// Config is this agent's configuration. See `AgentConfig' for additional
	// details.
	Config *AgentConfig
	// Systemd is this agent's systemd configuration. This MUST NOT be
	// specified in conjunction with the `Watchdog' configuration.
	Systemd *Systemd
	// Watchdog is this agent's watchdog configuration. This MUST NOT be
	// specified in conjunction with the `Systemd' configuration.
	Watchdog *Watchdog
}

// OpAMPServerConfig contains the connection details to talk to the OpAMP
// Server.
type OpAMPServerConfig struct {
	// Endpoint is the URL of the OpAMP server on which a WebSocket connection
	// is established to exchange OpAMP messages.
	Endpoint string
	// Tls contains the configuration required for client-side SSL/TLS encryption.
	Tls *tls.Config
}

// Configuration is the supervisor's configuration.
type Configuration struct {
	OpAMPServer *OpAMPServerConfig
	Agent       *Agent
}
