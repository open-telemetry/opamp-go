package types

import (
	"context"
	"io"
)

// AddonSyncer can be used by the agent to initiate syncing an addon from the server.
// The AddonSyncer instance knows the right context: the particular OpAMPClient and
// the particular AddonAvailable message the OnAddonAvailable callback was called for.
type AddonSyncer interface {
	// Sync the available addon from the server to the agent.
	// The agent must supply an AddonStateProvider to let the Sync function
	// know what is available locally, what data needs to be sync and how the
	// data can be stored locally.
	Sync(ctx context.Context, localState AddonStateProvider) error
}

type AddonStateProvider interface {
	AllAddonsHash() ([]byte, error)

	// Addons returns the names of all addons that exist in the agent's local storage.
	Addons() ([]string, error)

	// AddonHash returns the hash of a local addon. addonName is one of the names
	// that were returned by GetAddons().
	AddonHash(addonName string) ([]byte, error)

	// CreateAddon creates the addon locally. If the addon existed must return an error.
	// If the addon did not exist its hash should be set to nil.
	CreateAddon(addonName string) error

	// FileContentHash returns the content hash of the addon file that exists locally.
	FileContentHash(addonName string) ([]byte, error)

	// UpdateContent must create or update the addon content file. The entire content
	// of the file must be replaced by the data. The data must be read until
	// it returns an EOF. If reading from data fails UpdateContent must abort and return
	// an error.
	// Content hash must be updated if the data is updated without failure.
	// The function must cancel and return an error if the context is cancelled.
	UpdateContent(ctx context.Context, addonName string, data io.Reader, contentHash []byte) error

	// SetAddonHash must remember the hash for the specified addon. Must be returned
	// later when GetAddonHash is called. SetAddonHash is called after all UpsertFile
	// and DeleteFile calls complete successfully.
	SetAddonHash(addonName string, hash []byte) error

	// DeleteAddon deletes the addon from the agent's local storage.
	DeleteAddon(addonName string) error

	// SetAllAddonsHash must remember the AllAddonHash. Must be returned
	// later when AllAddonsHash is called. SetAllAddonsHash is called after all
	// addon updates complete successfully.
	SetAllAddonsHash(hash []byte) error
}
