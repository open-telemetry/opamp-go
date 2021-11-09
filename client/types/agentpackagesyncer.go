package types

import (
	"context"
	"io"
)

// AgentPackageSyncer can be used by the agent to initiate syncing an agent package from the server.
// The AgentPackageSyncer instance knows the right context: the particular OpAMPClient and
// the particular AgentPackageAvailable message the OnAgentPackageAvailable callback was called for.
type AgentPackageSyncer interface {
	// Sync the available package from the server to the agent.
	// The agent must supply an AgentPackageStateProvider to let the Sync function
	// know what is available locally, what data needs to be sync and how the
	// data can be stored locally.
	Sync(localAddonState *AgentPackageStateProvider) error
}

// AgentPackageStateProvider allows AgentPackageSyncer to assess the local state
// of an agent package, and perform the syncing process to ensure the local state
// matches exactly the package that is available on the server.
// It is assumed the agent can store only a single package locally.
type AgentPackageStateProvider interface {
	// PackageInfo returns the version of the locally available package the hash
	// of all local files, previously set via SetPackageInfo.
	PackageInfo() (version string, hash []byte, err error)

	// Files returns the names of the agent package files that exist locally.
	Files() ([]string, error)

	// FileHash returns the hash of the file that exists locally.
	FileHash(fileName string) ([]byte, error)

	// UpsertFile must create or update the specified local file. The entire content
	// of the file must be replaced by the data. The data must be read until
	// it returns an EOF.
	// The function must cancel and return an error if the context is cancelled.
	UpsertFile(ctx context.Context, fileName string, data io.Reader) error

	// DeleteFile must delete the specified local file.
	DeleteFile(fileName string) error

	// SetPackageInfo must remember the package hash and version. Hash and version
	// must be returned later when PackageInfo is called. SetPackageInfo is called
	// after all file updates complete successfully.
	SetPackageInfo(version string, hash []byte) error
}
