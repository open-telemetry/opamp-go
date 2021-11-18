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
	// PackageInfo returns the version of the locally available package and the hash
	// of the file content, previously set via UpdateContent.
	PackageInfo() (version string, contentHash []byte, err error)

	// UpdateContent must create or update the local content file. The entire content
	// of the file must be replaced by the data. The data must be read until
	// it returns an EOF. If reading from data fails UpdateContent must abort and return
	// an error.
	// Version and content hash must be updated if the data is updated without failure.
	// Version and content hash must be returned later when PackageInfo is called when
	// the next AgentPackageAvailable message arrives.
	// The function must cancel and return an error if the context is cancelled.
	UpdateContent(ctx context.Context, data io.Reader, contentHash []byte, version string) error
}
