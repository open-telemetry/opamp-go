package agent

import "context"

// CommandConfig describes a systemd-based or an exec-based way of starting,
// stopping and restarting the agent. Only one of `ExecConfig' or `SystemdConfig'
// must be specified.
type CommandConfig struct {
	// Name is the path to the Agent executable. Path can be relative
	// or absolute. A relative path should be relative to the CWD of the
	// supervisor.
	Name string `koanf:"name"`

	// Args are the set of command-line arguments that must be specified along
	// with the executable to start the Agent.
	Args []string `koanf:"args"`
}

// Process represents information related to a running agent process.
type Process struct {
	// PID is the process id of the agent process that is currently running.
	//
	// A watchdog may use this PID to stop this agent process at a later time.
	PID int

	// Done is the channel which is set when the process terminates. This allows
	// for async start of the agent followed by a continuous monitoring of its
	// termination.
	//
	// A watchdog, for instance, can wait on this channel to listen to process
	// termination event and restart the agent if needed.
	Done <-chan struct{}
}

// Commander allows the agent to be started, stopped and restarted at various
// points in the supervisor's lifetime.
type Commander interface {
	// Start the agent process and return the `Process' information. The
	// actual mechanism for starting the process depends on whether or not
	// the agent is managed by a `systemd'-like facility.
	//
	// Start MUST NOT block and return whether or not it was able to successfully
	// launch the agent process from an OS perspective, i.e., the process may
	// start but fail sometime later.
	//
	// Start returns the process information pertaining to the new agent process
	// or an error if the process could not be successfully restarted.
	Start() (*Process, error)

	// Stop the agent process as defined by `process'. The actual mechanism
	// for stopping the process depends on whether or not the agent is managed
	// by a `systemd'-like facility.
	//
	// Stop MUST wait for the process to be terminated from an OS perspective.
	Stop(ctx context.Context, process *Process) error

	// Restart the agent process as defined by `process'.
	//
	// Restart, most likely, is implemented as a Stop followed by a Start in
	// which case the Stop part might block until the process terminates.
	//
	// Restart returns the process information pertaining to the new agent process
	// or an error if the process could not be successfully restarted.
	Restart(
		ctx context.Context,
		process *Process,
	) (*Process, error)
}
