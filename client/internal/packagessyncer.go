package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// packagesSyncer performs the package syncing process.
type packagesSyncer struct {
	logger            types.Logger
	available         *protobufs.PackagesAvailable
	clientSyncedState *ClientSyncedState
	localState        types.PackagesStateProvider
	sender            Sender

	statuses *protobufs.PackageStatuses
	doneCh   chan struct{}
}

// NewPackagesSyncer creates a new packages syncer.
func NewPackagesSyncer(
	logger types.Logger,
	available *protobufs.PackagesAvailable,
	sender Sender,
	clientSyncedState *ClientSyncedState,
	packagesStateProvider types.PackagesStateProvider,
) *packagesSyncer {
	return &packagesSyncer{
		logger:            logger,
		available:         available,
		sender:            sender,
		clientSyncedState: clientSyncedState,
		localState:        packagesStateProvider,
		doneCh:            make(chan struct{}),
	}
}

// Sync performs the package syncing process.
func (s *packagesSyncer) Sync(ctx context.Context) error {

	defer func() {
		close(s.doneCh)
	}()

	// Prepare package statuses.
	if err := s.initStatuses(); err != nil {
		return err
	}

	if err := s.clientSyncedState.SetPackageStatuses(s.statuses); err != nil {
		return err
	}

	// Now do the actual syncing in the background.
	go s.doSync(ctx)

	return nil
}

func (s *packagesSyncer) Done() <-chan struct{} {
	return s.doneCh
}

// initStatuses initializes the "statuses" field from the current "localState".
// The "statuses" will be updated as the syncing progresses and will be
// periodically reported to the Server.
func (s *packagesSyncer) initStatuses() error {
	if s.localState == nil {
		return errors.New("cannot sync packages because PackagesStateProvider is not provided")
	}

	// Restore statuses that were previously stored in the local state.
	var err error
	s.statuses, err = s.localState.LastReportedStatuses()
	if err != nil {
		return err
	}
	if s.statuses == nil {
		// No statuses are stored locally (maybe first time), just start with empty.
		s.statuses = &protobufs.PackageStatuses{}
	}

	if s.statuses.Packages == nil {
		s.statuses.Packages = map[string]*protobufs.PackageStatus{}
	}

	// Report to the Server the "all" hash that we received from the Server so that
	// the Server knows we are processing the right offer.
	s.statuses.ServerProvidedAllPackagesHash = s.available.AllPackagesHash

	return nil
}

// doSync performs the actual syncing process.
func (s *packagesSyncer) doSync(ctx context.Context) {
	hash, err := s.localState.AllPackagesHash()
	if err != nil {
		s.logger.Errorf("Package syncing failed: %V", err)
		return
	}
	if bytes.Compare(hash, s.available.AllPackagesHash) == 0 {
		s.logger.Debugf("All packages are already up to date.")
		return
	}

	failed := false
	if err := s.deleteUnneededLocalPackages(); err != nil {
		s.logger.Errorf("Cannot delete unneeded packages: %v", err)
		failed = true
	}

	// Iterate through offered packages and sync them all from server.
	for name, pkg := range s.available.Packages {
		err := s.syncPackage(ctx, name, pkg)
		if err != nil {
			s.logger.Errorf("Cannot sync package %s: %v", name, err)
			failed = true
		}
	}

	if !failed {
		// Update the "all" hash on success, so that next time Sync() does not thing,
		// unless a new hash is received from the Server.
		if err := s.localState.SetAllPackagesHash(s.available.AllPackagesHash); err != nil {
			s.logger.Errorf("SetAllPackagesHash failed: %v", err)
		} else {
			s.logger.Debugf("All packages are synced and up to date.")
		}
	} else {
		s.logger.Errorf("Package syncing was not successful.")
	}

	_ = s.reportStatuses(true)
}

// syncPackage downloads the package from the server and installs it.
func (s *packagesSyncer) syncPackage(
	ctx context.Context,
	pkgName string,
	pkgAvail *protobufs.PackageAvailable,
) error {

	status := s.statuses.Packages[pkgName]
	if status == nil {
		// This package has no status. Create one.
		status = &protobufs.PackageStatus{
			Name:                 pkgName,
			ServerOfferedVersion: pkgAvail.Version,
			ServerOfferedHash:    pkgAvail.Hash,
		}
		s.statuses.Packages[pkgName] = status
	}
	// status.ServerOfferedVersion = pkgAvail.Version
	// status.ServerOfferedHash = pkgAvail.Hash

	pkgLocal, err := s.localState.PackageState(pkgName)
	if err != nil {
		return err
	}

	mustCreate := !pkgLocal.Exists
	if pkgLocal.Exists {
		if bytes.Equal(pkgLocal.Hash, pkgAvail.Hash) {
			s.logger.Debugf("Package %s hash is unchanged, skipping", pkgName)
			return nil
		}
		if pkgLocal.Type != pkgAvail.Type {
			// Package is of wrong type. Need to re-create it. So, delete it.
			if err := s.localState.DeletePackage(pkgName); err != nil {
				err = fmt.Errorf("cannot delete existing version of package %s: %v", pkgName, err)
				status.Status = protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed
				status.ErrorMessage = err.Error()
				return err
			}
			// And mark that it needs to be created.
			mustCreate = true
		}
	}

	// Report that we are beginning to install it.
	status.Status = protobufs.PackageStatusEnum_PackageStatusEnum_Installing
	_ = s.reportStatuses(true)

	if mustCreate {
		// Make sure the package exists.
		err = s.localState.CreatePackage(pkgName, pkgAvail.Type)
		if err != nil {
			err = fmt.Errorf("cannot create package %s: %v", pkgName, err)
			status.Status = protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed
			status.ErrorMessage = err.Error()
			return err
		}
	}

	// Sync package file: ensure it exists or download it.
	err = s.syncPackageFile(ctx, pkgName, pkgAvail.File)
	if err == nil {
		// Only save the state on success, so that next sync does not retry this package.
		pkgLocal.Hash = pkgAvail.Hash
		pkgLocal.Version = pkgAvail.Version
		if err := s.localState.SetPackageState(pkgName, pkgLocal); err == nil {
			status.Status = protobufs.PackageStatusEnum_PackageStatusEnum_Installed
			status.AgentHasHash = pkgAvail.Hash
			status.AgentHasVersion = pkgAvail.Version
		}
	}

	if err != nil {
		status.Status = protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed
		status.ErrorMessage = err.Error()
	}
	_ = s.reportStatuses(true)

	return err
}

// syncPackageFile downloads the package file from the server.
// If the file already exists and contents are
// unchanged, it is not downloaded again.
func (s *packagesSyncer) syncPackageFile(
	ctx context.Context, pkgName string, file *protobufs.DownloadableFile,
) error {
	shouldDownload, err := s.shouldDownloadFile(pkgName, file)
	if err == nil && shouldDownload {
		err = s.downloadFile(ctx, pkgName, file)
	}

	return err
}

// shouldDownloadFile returns true if the file should be downloaded.
func (s *packagesSyncer) shouldDownloadFile(
	packageName string,
	file *protobufs.DownloadableFile,
) (bool, error) {
	fileContentHash, err := s.localState.FileContentHash(packageName)

	if err != nil {
		err := fmt.Errorf("cannot calculate checksum of %s: %v", packageName, err)
		s.logger.Errorf(err.Error())
		return true, nil
	} else {
		// Compare the checksum of the file we have with what
		// we are offered by the server.
		if bytes.Compare(fileContentHash, file.ContentHash) != 0 {
			s.logger.Debugf("Package %s: file hash mismatch, will download.", packageName)
			return true, nil
		}
	}
	return false, nil
}

// downloadFile downloads the file from the server.
func (s *packagesSyncer) downloadFile(ctx context.Context, pkgName string, file *protobufs.DownloadableFile) error {
	s.logger.Debugf("Downloading package %s file from %s", pkgName, file.DownloadUrl)

	req, err := http.NewRequestWithContext(ctx, "GET", file.DownloadUrl, nil)
	if err != nil {
		return fmt.Errorf("cannot download file from %s: %v", file.DownloadUrl, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot download file from %s: %v", file.DownloadUrl, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cannot download file from %s, HTTP response=%v", file.DownloadUrl, resp.StatusCode)
	}

	// TODO: either add a callback to verify file.Signature or pass the Signature
	// as a parameter to UpdateContent.

	err = s.localState.UpdateContent(ctx, pkgName, resp.Body, file.ContentHash)
	if err != nil {
		return fmt.Errorf("failed to install/update the package %s downloaded from %s: %v", pkgName, file.DownloadUrl, err)
	}
	return nil
}

// deleteUnneededLocalPackages deletes local packages that are not
// needed anymore. This is done by comparing the local package state
// with the server's package state.
func (s *packagesSyncer) deleteUnneededLocalPackages() error {
	// Read the list of packages we have locally.
	localPackages, err := s.localState.Packages()
	if err != nil {
		return err
	}

	var lastErr error
	for _, localPkg := range localPackages {
		// Do we have a package that is not offered?
		if _, offered := s.available.Packages[localPkg]; !offered {
			s.logger.Debugf("Package %s is no longer needed, deleting.", localPkg)
			err := s.localState.DeletePackage(localPkg)
			if err != nil {
				lastErr = err
			}
		}
	}

	// Also remove packages that were not offered from the statuses.
	for name := range s.statuses.Packages {
		if _, offered := s.available.Packages[name]; !offered {
			delete(s.statuses.Packages, name)
		}
	}

	return lastErr
}

// reportStatuses saves the last reported statuses to provider and client state.
// If sendImmediately is true, the statuses are scheduled to be
// sent to the server.
func (s *packagesSyncer) reportStatuses(sendImmediately bool) error {
	// Save it in the user-supplied state provider.
	if err := s.localState.SetLastReportedStatuses(s.statuses); err != nil {
		s.logger.Errorf("Cannot save last reported statuses: %v", err)
		return err
	}

	// Also save it in our internal state (will be needed if the Server asks for it).
	if err := s.clientSyncedState.SetPackageStatuses(s.statuses); err != nil {
		s.logger.Errorf("Cannot save client state: %v", err)
		return err
	}
	s.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.PackageStatuses = s.clientSyncedState.PackageStatuses()
		})

	if sendImmediately {
		s.sender.ScheduleSend()
	}
	return nil
}
