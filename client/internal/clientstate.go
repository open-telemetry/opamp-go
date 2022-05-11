package internal

import (
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

// ClientSyncedState stores the state of the Agent messages that the OpAMP Client needs to
// have access to synchronize to the Server. 3 messages can be stored in this store:
// AgentDescription, RemoteConfigStatus and PackageStatuses.
//
// See OpAMP spec for more details on how state synchronization works:
// https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#agent-to-server-state-synchronization
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

	agentDescription   *protobufs.AgentDescription
	remoteConfigStatus *protobufs.RemoteConfigStatus
	packageStatuses    *protobufs.PackageStatuses
}

func (s *ClientSyncedState) AgentDescription() *protobufs.AgentDescription {
	defer s.mutex.Unlock()
	s.mutex.Lock()
	return s.agentDescription
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
		return errAgentDescriptionMissing
	}

	clone := proto.Clone(descr).(*protobufs.AgentDescription)

	if len(descr.Hash) == 0 {
		return errors.New("hash field must be set, use CalcHashAgentDescription")
	}

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.agentDescription = clone

	return nil
}

func calcHashRemoteConfigStatus(status *protobufs.RemoteConfigStatus) {
	// Calculate and set the Hash field from the rest of the fields in the message.
	h := sha256.New()
	h.Write(status.LastRemoteConfigHash)
	h.Write([]byte(status.Status.String()))
	h.Write([]byte(status.ErrorMessage))
	status.Hash = h.Sum(nil)
}

func (s *ClientSyncedState) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	if len(status.Hash) == 0 {
		return errors.New("hash field must be set, use CalcHashRemoteConfigStatus")
	}

	clone := proto.Clone(status).(*protobufs.RemoteConfigStatus)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.remoteConfigStatus = clone

	return nil
}
