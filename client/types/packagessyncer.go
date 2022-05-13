package types

import (
	"context"
	"io"
)

// PackagesSyncer can be used by the Agent to initiate syncing a package from the Server.
// The PackagesSyncer instance knows the right context: the particular OpAMPClient and
// the particular PackageAvailable message the OnPackageAvailable callback was called for.
type PackagesSyncer interface {
	// Sync the available package from the Server to the Agent.
	// The Agent must supply an PackagesStateProvider to let the Sync function
	// know what is available locally, what data needs to be sync and how the
	// data can be stored locally.
	Sync(ctx context.Context, localState PackagesStateProvider) error
}

type PackagesStateProvider interface {
	AllPackagesHash() ([]byte, error)

	// Packages returns the names of all packages that exist in the Agent's local storage.
	Packages() ([]string, error)

	// PackageHash returns the hash of a local package. packageName is one of the names
	// that were returned by Packages().
	PackageHash(packageName string) ([]byte, error)

	// CreatePackage creates the package locally. If the package existed must return an error.
	// If the package did not exist its hash should be set to nil.
	CreatePackage(packageName string) error

	// FileContentHash returns the content hash of the package file that exists locally.
	FileContentHash(packageName string) ([]byte, error)

	// UpdateContent must create or update the package content file. The entire content
	// of the file must be replaced by the data. The data must be read until
	// it returns an EOF. If reading from data fails UpdateContent must abort and return
	// an error.
	// Content hash must be updated if the data is updated without failure.
	// The function must cancel and return an error if the context is cancelled.
	UpdateContent(ctx context.Context, packageName string, data io.Reader, contentHash []byte) error

	// SetPackageHash must remember the hash for the specified package. Must be returned
	// later when PackageHash is called. SetPackageHash is called after UpdateContent
	// call completes successfully.
	SetPackageHash(packageName string, hash []byte) error

	// DeletePackage deletes the package from the Agent's local storage.
	DeletePackage(packageName string) error

	// SetAllPackagesHash must remember the AllPackagesHash. Must be returned
	// later when AllPackagesHash is called. SetAllPackagesHash is called after all
	// package updates complete successfully.
	SetAllPackagesHash(hash []byte) error
}
