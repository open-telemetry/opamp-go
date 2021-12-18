package agent

import (
	"context"
	"time"
)

// WatchConfig dictates the timing restrictions to be honored during an attempt
// to start, stop or restart the agent.
// restart the agent.
type WatchConfig struct {
	// CommandConfig describes how to exec the agent.
	CommandConfig *CommandConfig `koanf:"command"`

	// HealthCheckConfig describes how to monitor the agent's health.
	HealthCheckConfig *HealthCheckConfig `koanf:"health_check"`

	// MaxAttempts is the maximum number of attempts the agent is allowed to
	// fail a START, STOP or RESTART operation.
	MaxAttempts int `koanf:"max_attempts"`

	// WaitBetweenAttempts is the duration to wait between successive attempts
	// to START, STOP or RESTART the agent.
	WaitBetweenAttempts time.Duration `koanf:"wait_between_attempts"`
}

type Watcher interface {
	// Watch starts the agent and begins the monitoring of its health.
	//
	// Watch may block until the agent is started and the health check is
	// successful as described by the configuration.
	//
	// Watch MUST NOT block forever, i.e., any long running monitoring logic
	// must be done in a separate Go routine if necessary.
	//
	// Watch returns an error if the agent cannot be started per the specified
	// configuration or the initial health check fails.
	//
	// Watch should only be called once (preferably at the start of the
	// supervisor).
	Watch(ctx context.Context) error

	// Restart the monitored agent.
	//
	// Restart may block until the agent is restarted and the health check
	// is successful as described by the configuration.
	//
	// Restart MUST NOT block forever, i.e., any long running monitoring logic
	// post-restart must be done in a separate Go routine if necessary.
	//
	// Restart returns an error if the agent cannot be restarted per the
	// specified configuration
	Restart(ctx context.Context) error

	// Shutdown the watcher along with the monitoring of the agent's health.
	Shutdown(ctx context.Context) error
}