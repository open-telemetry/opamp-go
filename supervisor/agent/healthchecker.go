package agent

import "time"

// HealthCheckConfig describes how the agent's health is monitored.
type HealthCheckConfig struct {
	// URL at which the health-check must be done. The health check is considered
	// successful if a GET on the URL responds with a code between 200 and 400.
	URL          string        `koanf:"url"`

	// InitialDelay is duration that the supervisor should wait before making
	// the first health-check after a Start or Restart of the agent.
	InitialDelay time.Duration `koanf:"initial_delay"`

	// Period is the interval at which the supervisor should make the
	// health-check requests.
	Period       time.Duration `koanf:"period"`

	// Timeout is the duration within which the agent must respond to a
	// health-check after which it is deemed to have failed.
	Timeout      time.Duration `koanf:"timeout"`

	// SuccessThreshold is the minimum consecutive successes for the health-check
	// to be considered successful after having failed.
	SuccessThreshold int `koanf:"success_threshold"`

	// FailureThreshold is the minimum number of times the health-check must
	// fail after the initial failure for it to be restarted.
	FailureThreshold int `koanf:"failure_threshold"`
}

// HealthChecker allows the agent's health, as described by the HealthCheckConfig,
// to be monitored continuously after a Start or Restart.
type HealthChecker interface {
	// IsHealthy returns true if the agent is healthy according to the health
	// check configuration and false otherwise.
	IsHealthy() bool
}
