package supervisor

import (
	"context"
	"github.com/open-telemetry/opamp-go/supervisor/types"
)

// Supervisor allows the management of an agent. Specific agents like an
// OpenTelemetry collector can implement a custom supervisor that understands
// the internal workings of the collector. Alternatively, a default supervisor
// allows for the supervision of arbitrary daemon-like processes and provides
// management options like remote configuration change, agent upgrades,
// watchdog capabilities, telemetry reporting, etc.
//
// The supervisor wraps the client part of the OpAMP protocol and therefore
// requires an OpAMP server to be running at some endpoint. There is a 1:1
// mapping between a supervisor and the OpAMP client. A single supervisor
// executable, however, can run several supervisors within it thereby managing
// several distinct agent types.
//
// See the supervisor sample configuration file and the associated
// `types.Configuration` for details.
type Supervisor interface {
	// Start the supervisor and begin monitoring the associated agent as
	// specified by the configuration.
	//
	// Start MUST return immediately (potentially with an error if the specified
	// configuration is incorrect or it fails to parse).
	//
	// Start, in most cases, should spawn a Go routine to monitor the agent
	// under supervision.
	//
	// Start, on a given instance, must be called exactly once. Subsequent
	// calls have no effect.
	Start(config *types.Configuration) error

	// Stop this supervisor and as a result stop monitoring the associated
	// agent. This method is typically called on receiving an async OS
	// termination signal like SIGINT or SIGTERM.
	Stop(ctx context.Context) error
}
