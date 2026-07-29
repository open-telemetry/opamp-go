package internal

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

// Sentinel errors returned by attestationState. Callers can use
// errors.Is to distinguish failure modes when terminating the
// connection.
var (
	// ErrMissingTrustChain is returned when the first
	// SignedServerToAgent received on a connection does not carry a
	// trust_chain_response field. Per the spec this is a fatal
	// handshake error.
	ErrMissingTrustChain = errors.New("client: first SignedServerToAgent missing trust_chain_response")

	// ErrTrustChainErrorReported is returned when the Server populates
	// trust_chain_response.error_message, signalling that it cannot
	// satisfy the handshake.
	ErrTrustChainErrorReported = errors.New("client: server reported trust chain error")

	// ErrSANMismatch is returned when the leaf certificate's Subject
	// Alternative Name entries do not contain a dNSName or iPAddress
	// that matches the OpAMP server the Agent is connected to. Per the
	// spec this is a fatal handshake error.
	ErrSANMismatch = errors.New("client: leaf certificate SAN does not match server hostname")

	// ErrServerNameUnavailable is returned when payload trust
	// verification is enabled but the Agent could not determine the
	// server hostname to check the leaf certificate's SAN against (for
	// example, the server URL was empty or unparseable). SAN
	// verification is mandatory when attestation is on, so rather than
	// silently skip it the handshake fails closed.
	ErrServerNameUnavailable = errors.New("client: cannot verify leaf certificate SAN: server hostname unavailable")

	// ErrTOFUAnchorMissing is returned during TOFU enrollment when the
	// Server's TrustChainResponse does not include the expected
	// tofu_trust_anchor field.
	ErrTOFUAnchorMissing = errors.New("client: TOFU enrollment requested but TrustChainResponse.tofu_trust_anchor is absent")

	// ErrMissingSignature is returned when a SignedServerToAgent is
	// missing its signature field. Every message MUST be signed,
	// including the first.
	ErrMissingSignature = errors.New("client: SignedServerToAgent missing signature")

	// ErrMissingPayload is returned when SignedServerToAgent.payload
	// is empty. The payload carries the inner ServerToAgent; an empty
	// payload would unmarshal into an empty ServerToAgent and is
	// rejected eagerly.
	ErrMissingPayload = errors.New("client: SignedServerToAgent missing payload")

	// ErrEmptyInnerServerToAgent is returned when the inner payload
	// decodes to a ServerToAgent with all default values. Defends
	// against the proto3 field-1 wire-type collision: a malicious
	// server that downgrades by responding with a plain ServerToAgent
	// has its InstanceUid bytes misinterpreted as
	// SignedServerToAgent.payload; the inner decode of those random
	// UUID bytes either errors or produces a default-valued message.
	// Legitimate server responses always carry at least InstanceUid
	// because handleWSConnection auto-fills it (see
	// server/serverimpl.go).
	ErrEmptyInnerServerToAgent = errors.New("client: inner ServerToAgent decoded to all default values; likely downgrade attempt")
)

// attestationState holds per-connection state for payload trust
// verification on the Agent (client) side. Construct one per OpAMP
// connection via newAttestationState and call ProcessEnvelope on each
// inbound SignedServerToAgent.
//
// When Verifier is nil (the operator did not opt in), the OpAMP wire
// format is the standard ServerToAgent protobuf and no attestationState
// is created at all; payload trust is simply not negotiated.
type attestationState struct {
	verifier   signing.Verifier
	serverName string               // hostname for SAN verification
	enroller   signing.TOFUEnroller // non-nil when in TOFU enrollment mode

	mu             sync.Mutex
	firstSeen      bool
	leaf           *x509.Certificate
	pinnedChainPEM []byte
}

// newAttestationState constructs a per-connection attestation state.
// verifier is nil in TOFU enrollment mode (enroller non-nil); in that case
// the verifier is bootstrapped from the first TrustChainResponse via the
// enroller. serverName is the hostname (without port) of the OpAMP server.
func newAttestationState(verifier signing.Verifier, serverName string, enroller signing.TOFUEnroller) *attestationState {
	return &attestationState{verifier: verifier, serverName: serverName, enroller: enroller}
}

// Reset clears the per-connection handshake state. After Reset, the
// next call to ProcessEnvelope is treated as if it were the first
// message on the connection — requiring trust_chain_response and
// performing a fresh chain validation.
//
// Used by transports that lack a persistent connection (the HTTP
// polling transport) to recover from server-side key rotation or
// other mid-stream handshake faults. WebSocket callers do not need
// to call Reset because a failure terminates the connection and the
// next reconnect attempt constructs a new attestationState.
func (s *attestationState) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstSeen = false
	s.leaf = nil
	s.pinnedChainPEM = nil
}

// isAttestationFailure reports whether err originated from a payload
// trust verification problem (envelope malformed, chain validation
// failed, signature missing or invalid, etc.). Used by the WebSocket
// receive loop to distinguish attestation failures — which require
// explicit connection termination per the spec — from generic
// transport-level errors.
func isAttestationFailure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrMissingTrustChain) ||
		errors.Is(err, ErrTrustChainErrorReported) ||
		errors.Is(err, ErrSANMismatch) ||
		errors.Is(err, ErrTOFUAnchorMissing) ||
		errors.Is(err, ErrMissingSignature) ||
		errors.Is(err, ErrMissingPayload) ||
		errors.Is(err, ErrEmptyInnerServerToAgent) ||
		errors.Is(err, signing.ErrChainValidation) ||
		errors.Is(err, signing.ErrSignatureMismatch) ||
		errors.Is(err, signing.ErrEmptyChain) ||
		errors.Is(err, signing.ErrParseCertificate) ||
		errors.Is(err, signing.ErrUnsupportedAlgorithm)
}

// ProcessEnvelope handles an incoming SignedServerToAgent received on
// this connection. On the first call, the envelope's certificate
// chain is validated against the verifier's pre-configured trust
// anchor pool and the resulting leaf is cached on the state. On
// subsequent calls, the envelope's signature is verified against the
// cached leaf.
//
// On success it returns the inner ServerToAgent payload bytes, which
// the caller unmarshals into a *protobufs.ServerToAgent for normal
// dispatch.
//
// On any failure — missing trust chain, chain validation failure,
// missing/invalid signature — it returns a non-nil error. Per the
// spec the caller MUST then terminate the OpAMP connection.
func (s *attestationState) ProcessEnvelope(ctx context.Context, envelope *protobufs.SignedServerToAgent) ([]byte, error) {
	if envelope == nil {
		return nil, errors.New("client: nil SignedServerToAgent envelope")
	}
	if len(envelope.Payload) == 0 {
		return nil, ErrMissingPayload
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if chainResp := envelope.TrustChainResponse; chainResp != nil &&
		!(s.firstSeen && bytes.Equal(chainResp.CertificateChain, s.pinnedChainPEM)) {
		if chainResp.ErrorMessage != "" {
			return nil, fmt.Errorf("%w: %s", ErrTrustChainErrorReported, chainResp.ErrorMessage)
		}
		chainDER, err := parsePEMChain(chainResp.CertificateChain)
		if err != nil {
			return nil, fmt.Errorf("client: parse trust chain PEM: %w", err)
		}

		if s.enroller != nil {
			if len(chainResp.TofuTrustAnchor) == 0 {
				return nil, ErrTOFUAnchorMissing
			}
			v, err := s.enroller.Enroll(chainResp.TofuTrustAnchor)
			if err != nil {
				return nil, fmt.Errorf("client: TOFU enrollment: %w", err)
			}
			s.verifier = v
			s.enroller = nil
		}

		leaf, err := s.verifier.ValidateChain(ctx, chainDER, time.Now())
		if err != nil {
			return nil, fmt.Errorf("client: validate trust chain: %w", err)
		}
		if s.serverName == "" {
			return nil, ErrServerNameUnavailable
		}
		if err := leaf.VerifyHostname(s.serverName); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSANMismatch, err)
		}
		s.leaf = leaf
		s.pinnedChainPEM = chainResp.CertificateChain
		s.firstSeen = true
	} else if !s.firstSeen {
		return nil, ErrMissingTrustChain
	}

	// Every message — including the first — MUST carry a signature.
	if len(envelope.Signature) == 0 {
		return nil, ErrMissingSignature
	}
	if err := s.verifier.Verify(ctx, envelope.Payload, envelope.Signature, s.leaf); err != nil {
		return nil, fmt.Errorf("client: verify signature: %w", err)
	}
	return envelope.Payload, nil
}

// parsePEMChain decodes a concatenated PEM blob into individual DER byte
// slices ordered intermediates-first, leaf-last — the form expected by
// signing.Verifier.ValidateChain.
func parsePEMChain(pemBytes []byte) ([][]byte, error) {
	var chain [][]byte
	rest := pemBytes
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		chain = append(chain, block.Bytes)
	}
	if len(chain) == 0 {
		return nil, errors.New("no CERTIFICATE blocks found in PEM")
	}
	return chain, nil
}

// unwrapServerToAgent is a convenience that combines ProcessEnvelope
// with proto.Unmarshal of the resulting payload bytes into msg. If
// state is nil, the input bytes are unmarshalled directly as a
// ServerToAgent (the standard non-attestation path).
//
// rawProto is the protobuf bytes after any transport-level framing
// has been stripped (for WebSocket, after the wsMsgHeader varint).
func unwrapServerToAgent(ctx context.Context, state *attestationState, rawProto []byte, msg *protobufs.ServerToAgent) error {
	if state == nil {
		return proto.Unmarshal(rawProto, msg)
	}
	var envelope protobufs.SignedServerToAgent
	if err := proto.Unmarshal(rawProto, &envelope); err != nil {
		return fmt.Errorf("client: decode SignedServerToAgent envelope: %w", err)
	}
	payload, err := state.ProcessEnvelope(ctx, &envelope)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(payload, msg); err != nil {
		return fmt.Errorf("client: decode inner ServerToAgent: %w", err)
	}
	// Defense in depth against proto3 field-1 wire-type collision.
	// ProcessEnvelope's chain/signature checks already terminate the
	// connection on the downgrade path that produces this state, but
	// this check pins the contract: every legitimate ServerToAgent
	// the agent processes has at least one non-default field
	// (typically InstanceUid).
	if proto.Equal(msg, &protobufs.ServerToAgent{}) {
		return ErrEmptyInnerServerToAgent
	}
	return nil
}
