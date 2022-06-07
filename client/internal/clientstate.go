package internal

import (
	"crypto/sha256"
	"errors"
	"hash"
	"sort"
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	errRemoteConfigStatusMissing        = errors.New("RemoteConfigStatus is not set")
	errLastRemoteConfigHashNil          = errors.New("LastRemoteConfigHash is nil")
	errPackageStatusesMissing           = errors.New("PackageStatuses is not set")
	errServerProvidedAllPackagesHashNil = errors.New("ServerProvidedAllPackagesHash is nil")
)

// ClientSyncedState stores the state of the Agent messages that the OpAMP Client needs to
// have access to synchronize to the Server. 3 messages can be stored in this store:
// AgentDescription, RemoteConfigStatus and PackageStatuses.
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

func writeHashKV(hash hash.Hash, kv *protobufs.KeyValue) error {
	// To keep the implementation simple we convert the data to an equivalent JSON
	// string and calculate the hash from the string bytes.
	b, err := protojson.Marshal(kv)
	if err != nil {
		return err
	}
	hash.Write(b)
	return nil
}

func writeHashKVList(hash hash.Hash, kvl []*protobufs.KeyValue) error {
	for _, kv := range kvl {
		err := writeHashKV(hash, kv)
		if err != nil {
			return err
		}
	}
	return nil
}

func calcHashAgentDescription(msg *protobufs.AgentDescription) error {
	h := sha256.New()
	err := writeHashKVList(h, msg.IdentifyingAttributes)
	if err != nil {
		return err
	}
	err = writeHashKVList(h, msg.NonIdentifyingAttributes)
	if err != nil {
		return err
	}
	msg.Hash = h.Sum(nil)
	return nil
}

// SetAgentDescription sets the AgentDescription in the state.
// Will calculate the Hash from the content of the other fields.
func (s *ClientSyncedState) SetAgentDescription(descr *protobufs.AgentDescription) error {
	if descr == nil {
		return ErrAgentDescriptionMissing
	}

	if descr.IdentifyingAttributes == nil && descr.NonIdentifyingAttributes == nil {
		return ErrAgentDescriptionNoAttributes
	}

	if err := calcHashAgentDescription(descr); err != nil {
		return err
	}

	clone := proto.Clone(descr).(*protobufs.AgentDescription)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.agentDescription = clone

	return nil
}

func calcHashRemoteConfigStatus(status *protobufs.RemoteConfigStatus) {
	h := sha256.New()
	h.Write(status.LastRemoteConfigHash)
	h.Write([]byte(status.Status.String()))
	h.Write([]byte(status.ErrorMessage))
	status.Hash = h.Sum(nil)
}

// SetRemoteConfigStatus sets the RemoteConfigStatus in the state.
// Will calculate the Hash from the content of the other fields.
func (s *ClientSyncedState) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	if status == nil {
		return errRemoteConfigStatusMissing
	}

	calcHashRemoteConfigStatus(status)

	clone := proto.Clone(status).(*protobufs.RemoteConfigStatus)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.remoteConfigStatus = clone

	return nil
}

func calcHashPackageStatuses(status *protobufs.PackageStatuses) {
	h := sha256.New()

	// Convert package names to slice to sort it and make sure hash calculation is
	// deterministic.

	var names []string
	for name := range status.Packages {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		pkg := status.Packages[name]
		h.Write([]byte(name))
		h.Write([]byte(pkg.AgentHasVersion))
		h.Write(pkg.AgentHasHash)
		h.Write([]byte(pkg.ServerOfferedVersion))
		h.Write(pkg.ServerOfferedHash)
		h.Write([]byte(pkg.Status.String()))
		h.Write([]byte(pkg.ErrorMessage))
	}

	h.Write(status.ServerProvidedAllPackagesHash)

	status.Hash = h.Sum(nil)
}

// SetPackageStatuses sets the PackageStatuses in the state.
// Will calculate the Hash from the content of the other fields.
func (s *ClientSyncedState) SetPackageStatuses(status *protobufs.PackageStatuses) error {
	if status == nil {
		return errPackageStatusesMissing
	}

	calcHashPackageStatuses(status)

	clone := proto.Clone(status).(*protobufs.PackageStatuses)

	defer s.mutex.Unlock()
	s.mutex.Lock()
	s.packageStatuses = clone

	return nil
}
