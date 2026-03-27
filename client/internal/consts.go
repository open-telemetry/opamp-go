package internal

import "github.com/open-telemetry/opamp-go/protobufs"

const (
	headerContentType      = "Content-Type"
	contentTypeProtobuf    = "application/x-protobuf"
	headerOpAMPInstanceUID = "OpAMP-Instance-UID"
)

// Only these capabilities are allowed to be changed after Start().
// See https://github.com/open-telemetry/opamp-spec/pull/306
const permittedCapabilityChanges = protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig
