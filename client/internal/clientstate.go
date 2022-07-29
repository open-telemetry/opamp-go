package internal

import (
	"errors"
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

var (
	errRemoteConfigStatusMissing        = errors.New("RemoteConfigStatus is not set")
	errLastRemoteConfigHashNil          = errors.New("LastRemoteConfigHash is nil")
	errPackageStatusesMissing           = errors.New("PackageStatuses is not set")
	errServerProvidedAllPackagesHashNil = errors.New("ServerProvidedAllPackagesHash is nil")
)

// ClientSyncedState stores the state of the Agent messages that the OpAMP Client needs to
// have access to synchronize to the Server. 4 messages can be stored in this store:
// AgentDescription, AgentHealth, RemoteConfigStatus and PackageStatuses.
//
// See OpAMP spec for more details on how state synchronization works:
// https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#Agent-to-Server-state-synchronization
//
// Note that the EffectiveConfig is subject to the same synchronization logic, however
// it is not stored in this struct since it can be large, and we do not want to always
// keep it in memory. To avoid storing it in memory the EffectiveConfig is supposed to be
// stored by the Agent implementation (e.g. it can be stored on disk) and is fetched
// via GetEffectiveConfig callback when it is needed by OpAMP client and then it is
// discarded from memory. See implementation of UpdateEffectiveConfig().
//
// It is safe to call methods of this struct concurrently.
type ClientSyncedState struct {
	mutex sync.Mutex
	packageSyncMutex sync.Mutex

	agentDescription   *protobufs.AgentDescription
	health             *protobufs.AgentHealth
	remoteConfigStatus *protobufs.RemoteConfigStatus
	packageStatuses    *protobufs.PackageStatuses
}

func (s *ClientSyncedState) AgentDescription() *protobufs.AgentDescription {
	defer s.mutex.Unlock()
	s.mutex.Lock()
	return s.agentDescription
}

func (s *ClientSyncedState) Health() *protobufs.AgentHealth {
	defer s.mutex.Unlock()
	s.mutex.Lock()
	return s.health
}

func (s *ClientSyncedState) RemoteConfigStatus() *protobufs.RemoteConfigStatus {
	defer s.mutex.Unlock()
	s.mutex.Lock()
	return s.remoteConfigStatus
}

func (s *ClientSyncedState) PackageStatuses() *protobufs.PackageStatuses {
	defer s.mutex.Unlock()
	s.mutex.Lock()
	return s.packageStatuses
}

// SetAgentDescription sets the AgentDescription in the state.
func (s *ClientSyncedState) SetAgentDescription(descr *protobufs.AgentDescription) error {
	if descr == nil {
		return ErrAgentDescriptionMissing
	}

	if descr.IdentifyingAttributes == nil && descr.NonIdentifyingAttributes == nil {
		return ErrAgentDescriptionNoAttributes
	}

	clone := proto.Clone(descr).(*protobufs.AgentDescription)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.agentDescription = clone

	return nil
}

// SetHealth sets the AgentHealth in the state.
func (s *ClientSyncedState) SetHealth(health *protobufs.AgentHealth) error {
	if health == nil {
		return ErrAgentHealthMissing
	}

	clone := proto.Clone(health).(*protobufs.AgentHealth)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.health = clone

	return nil
}

// SetRemoteConfigStatus sets the RemoteConfigStatus in the state.
func (s *ClientSyncedState) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	if status == nil {
		return errRemoteConfigStatusMissing
	}

	clone := proto.Clone(status).(*protobufs.RemoteConfigStatus)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.remoteConfigStatus = clone

	return nil
}

// SetPackageStatuses sets the PackageStatuses in the state.
func (s *ClientSyncedState) SetPackageStatuses(status *protobufs.PackageStatuses) error {
	if status == nil {
		return errPackageStatusesMissing
	}

	clone := proto.Clone(status).(*protobufs.PackageStatuses)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.packageStatuses = clone

	return nil
}
