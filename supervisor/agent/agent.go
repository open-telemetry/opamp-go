package agent

import "github.com/open-telemetry/opamp-go/protobufs"

type Settings struct {
	// Capabilities are the list of capabilities that this agent supports.
	// See `protobufs.AgentCapabilities'.
	Capabilities []string `koanf:"capabilities"`

	// Attrs are any key-value pair that describes the identifying and
	// non-identifying attributes of the agent.
	Attrs map[string]string `koanf:"attrs"`

	// ConfigSpec describes this agent's configuration source from which
	// its effective configuration is read and to which the remote configuration
	// is written.
	ConfigSpec *ConfigSpec `koanf:"config"`

	// PackageSpec describes the set of files that make this agent.
	PackageSpec *PackageSpec `koanf:"package"`
}

// Identifier provides this agent's unique identification.
type Identifier interface {
	// Type returns the FQDN of this agent. For example, for an OpenTelemetry
	// Collector this should return "io.opentelemetry.collector".
	Type() string

	// Version returns this agent's build version.
	//
	// Version may return an error if it is unable to determine the Agent's
	// version. This is possible, for example, if the only way to retrieve
	// the agent's version is to make an HTTP request and the request fails.
	Version() (string, error)

	// InstanceID uniquely identifies this agent in combination with other
	// attributes.
	InstanceID() (string, error)

	// Namespace return this agent's namespace. For example, for an OpenTelemetry
	// Collector, this is equivalent to `service.namespace'.
	Namespace() string
}

// Agent represents a long running process that allows for management via a
// supervisor. OpenTelemetry Collector or a FluentBit daemon are some of the
// examples of an Agent.
type Agent interface {
	// Identifier allows several identifying attributes like the Agent's
	// type, version and namespace to be returned.
	Identifier

	// GetNonIdentifyingAttrs returns attributes that do not necessarily help
	// identify the agent, but describe where it runs.
	GetNonIdentifyingAttrs() ([]*protobufs.KeyValue, error)

	// Commander allows the agent to be started, stopped and restarted.
	// See `agent.Commander`
	Commander

	// HealthChecker allows the agent's health to be monitored.
	// See `agent.HealthChecker`
	HealthChecker
}
