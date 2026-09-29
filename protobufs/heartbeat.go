package protobufs

import "google.golang.org/protobuf/proto"

// IsHeartbeatServerToAgent reports whether m is a heartbeat response: a
// ServerToAgent with only instance_uid set. This is the one shape that MAY
// be sent unsigned on a Message Attestation connection, and both the Server
// and the Agent use this definition. The check is default-deny: any other
// set field, including fields added to ServerToAgent later, disqualifies m.
func IsHeartbeatServerToAgent(m *ServerToAgent) bool {
	if m == nil || len(m.InstanceUid) == 0 {
		return false
	}
	probe := proto.Clone(m).(*ServerToAgent)
	probe.InstanceUid = nil
	return proto.Equal(probe, &ServerToAgent{})
}
