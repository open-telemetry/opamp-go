package agent

import (
	"context"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// PackageSpec describes what constitutes an agent package and is used to
// implement the `protobufs.AgentPackageAvailable' message.
type PackageSpec struct {
	// Files are the set of, potential binary files, that make up the agent's
	// package and must be updated as part of the `protobufs.AgentPackageAvailable'
	// message.
	Files map[string]string `koanf:"files"`
}

// Packager allows the agent's local package to be upgraded or downgraded from
// a remote file.
type Packager interface {
	// Sync the remote package described in `protobufs.AgentPackageAvailable'
	// with the locally available package as described by `PackageSpec'.
	//
	// Sync may check the content hash in the provided message to verify the
	// integrity of the downloaded package and to avoid unnecessary downloads.
	//
	// Sync may check the version in the the provided message to avoid
	// unnecessary downloads if the remote and local versions are the same.
	//
	// Sync may report download progress via the `onProgressFunc` callback.
	//
	// Sync may use the watcher to restart the agent after performing the
	// necessary updates.
	//
	// It is recommended for Sync to take a backup of the files being updated
	// so as to revert to it if the update is unsuccessful.
	//
	// Sync MUST return error if the package could not be downloaded, verified,
	// or installed.
	Sync(
		ctx context.Context,
		pkg *protobufs.AgentPackageAvailable,
		watcher Watcher,
		onProgressFunc func(file string, percent float32)) error
}
