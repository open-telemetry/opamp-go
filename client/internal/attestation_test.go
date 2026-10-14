package internal

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// On an attested connection (state != nil) an unsigned heartbeat
// (instance_uid only) is accepted without any signature/chain check.
func TestUnwrapServerToAgent_UnsignedHeartbeatAccepted(t *testing.T) {
	uid := []byte("0123456789abcdef")
	// verifier is nil: the heartbeat path must never touch it.
	state := newAttestationState(nil, "example.com", nil)
	raw := mustMarshal(t, &protobufs.ServerToAgent{InstanceUid: uid})

	var msg protobufs.ServerToAgent
	if err := unwrapServerToAgent(context.Background(), state, raw, &msg); err != nil {
		t.Fatalf("expected heartbeat to be accepted, got %v", err)
	}
	if string(msg.InstanceUid) != string(uid) {
		t.Fatalf("instance_uid = %q, want %q", msg.InstanceUid, uid)
	}
}

// An unsigned message carrying any actionable field is rejected
// fail-closed as an attestation failure, and msg is left cleared.
func TestUnwrapServerToAgent_UnsignedNonHeartbeatRejected(t *testing.T) {
	uid := []byte("0123456789abcdef")
	cases := map[string]*protobufs.ServerToAgent{
		"flags":           {InstanceUid: uid, Flags: 1},
		"capabilities":    {InstanceUid: uid, Capabilities: 1},
		"custom_caps":     {InstanceUid: uid, CustomCapabilities: &protobufs.CustomCapabilities{Capabilities: []string{"x"}}},
		"no_instance_uid": {Flags: 1},
		"fully_empty":     {},
	}
	for name, sta := range cases {
		t.Run(name, func(t *testing.T) {
			state := newAttestationState(nil, "example.com", nil)
			raw := mustMarshal(t, sta)

			var msg protobufs.ServerToAgent
			err := unwrapServerToAgent(context.Background(), state, raw, &msg)
			if !errors.Is(err, ErrUnsignedNonHeartbeat) {
				t.Fatalf("err = %v, want ErrUnsignedNonHeartbeat", err)
			}
			if !isAttestationFailure(err) {
				t.Fatalf("ErrUnsignedNonHeartbeat must be treated as an attestation failure")
			}
			if !proto.Equal(&msg, &protobufs.ServerToAgent{}) {
				t.Fatalf("msg must be cleared on rejection, got %v", &msg)
			}
		})
	}
}

// A malformed signed envelope (signature present, payload empty) is not a
// heartbeat candidate: it falls through to ProcessEnvelope and is
// rejected with ErrMissingPayload rather than silently accepted.
func TestUnwrapServerToAgent_EnvelopeWithSignatureNoPayload(t *testing.T) {
	state := newAttestationState(nil, "example.com", nil)
	raw := mustMarshal(t, &protobufs.SignedServerToAgent{Signature: []byte{0x01, 0x02}})

	var msg protobufs.ServerToAgent
	err := unwrapServerToAgent(context.Background(), state, raw, &msg)
	if !errors.Is(err, ErrMissingPayload) {
		t.Fatalf("err = %v, want ErrMissingPayload", err)
	}
}

// With no attestation state, bytes are unmarshalled directly as a
// ServerToAgent (the standard non-attestation path), including a message
// that would not qualify as a heartbeat.
func TestUnwrapServerToAgent_NilStatePassthrough(t *testing.T) {
	raw := mustMarshal(t, &protobufs.ServerToAgent{InstanceUid: []byte("x"), Flags: 7})

	var msg protobufs.ServerToAgent
	if err := unwrapServerToAgent(context.Background(), nil, raw, &msg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if msg.Flags != 7 {
		t.Fatalf("flags = %d, want 7", msg.Flags)
	}
}
