package protobufs

import "google.golang.org/protobuf/proto"

// IsHeartbeatServerToAgent reports whether m is a heartbeat response: a
// ServerToAgent in which only instance_uid is set and every other field
// is unset/default.
//
// This is the base protocol's acknowledgement-of-receipt response — the
// message a Server sends when it has no data to return, with all fields
// except instance_uid unset (see the ServerToAgent Message section of the
// OpAMP specification). It is the same shape the Agent receives on every
// HTTP poll, named "heartbeat response" by the Message Attestation
// Heartbeat Response Exemption.
//
// It is the exact shape that MAY be sent unsigned on a Message
// Attestation connection. The check is structural and default-deny: it
// clones the message, clears instance_uid, and requires the remainder to
// equal an empty ServerToAgent. Any other field that is set — including
// any field added to ServerToAgent in the future — disqualifies the
// message from the exemption, so new fields can never silently become
// part of the unsigned surface.
//
// Both the Server (deciding what it MAY send unsigned) and the Agent
// (deciding what it MAY accept unsigned) use this single definition.
func IsHeartbeatServerToAgent(m *ServerToAgent) bool {
	if m == nil || len(m.InstanceUid) == 0 {
		return false
	}
	probe := proto.Clone(m).(*ServerToAgent)
	probe.InstanceUid = nil
	return proto.Equal(probe, &ServerToAgent{})
}
