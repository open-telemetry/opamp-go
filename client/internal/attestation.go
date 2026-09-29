package internal

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

// Errors returned by the attestation path. Every one of them is also an
// attestation failure (see isAttestationFailure).
var (
	// ErrMissingTrustChain: the first signed message lacks trust_chain_response.
	ErrMissingTrustChain = errors.New("client: first SignedServerToAgent missing trust_chain_response")

	// ErrTrustChainErrorReported: the server set trust_chain_response.error_message.
	ErrTrustChainErrorReported = errors.New("client: server reported trust chain error")

	// ErrTOFUAnchorMissing: TOFU enrollment is pending but tofu_trust_anchor is absent.
	ErrTOFUAnchorMissing = errors.New("client: TOFU enrollment requested but TrustChainResponse.tofu_trust_anchor is absent")

	// ErrMissingSignature: a signed envelope has no signature.
	ErrMissingSignature = errors.New("client: SignedServerToAgent missing signature")

	// ErrMissingPayload: a signed envelope has no payload.
	ErrMissingPayload = errors.New("client: SignedServerToAgent missing payload")

	// ErrEmptyInnerServerToAgent: a verified payload decodes to an empty
	// ServerToAgent. Legitimate messages always carry at least InstanceUid.
	ErrEmptyInnerServerToAgent = errors.New("client: inner ServerToAgent decoded to all default values")

	// ErrMalformedEnvelope: the message does not decode as a SignedServerToAgent.
	ErrMalformedEnvelope = errors.New("client: malformed SignedServerToAgent envelope")

	// ErrMalformedTrustChain: the delivered chain has no decodable certificates.
	ErrMalformedTrustChain = errors.New("client: malformed trust_chain_response certificate chain")

	// ErrTOFUEnrollment: the offered anchor is unusable, the chain or first
	// signature does not verify under it, or it cannot be persisted.
	ErrTOFUEnrollment = errors.New("client: TOFU enrollment failed")

	// ErrUnsignedNonHeartbeat: an unsigned message other than a heartbeat
	// response, treated as a downgrade attempt.
	ErrUnsignedNonHeartbeat = errors.New("client: unsigned ServerToAgent is not a heartbeat; rejecting non-signed message")
)

// attestationState holds the Agent's per-connection payload trust state. It
// exists only when attestation is enabled.
type attestationState struct {
	verifier   signing.Verifier
	serverName string               // host matched against the leaf's SANs
	enroller   signing.TOFUEnroller // non-nil while TOFU enrollment is pending

	mu             sync.Mutex
	firstSeen      bool
	verified       *signing.VerifiedCertificate
	pinnedChainPEM []byte
	verifiedOnce   bool // not cleared by Reset
}

// HasVerified reports whether any signed message has verified.
func (s *attestationState) HasVerified() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verifiedOnce
}

// newAttestationState returns state with either a verifier or, for TOFU, an
// enroller. serverName is the server host, without port.
func newAttestationState(verifier signing.Verifier, serverName string, enroller signing.TOFUEnroller) *attestationState {
	return &attestationState{verifier: verifier, serverName: serverName, enroller: enroller}
}

// Reset clears the handshake state so the next message must carry a trust
// chain again. The HTTP transport, which has no connection to drop, uses it
// to recover after a failure; WebSocket reconnects get a fresh state.
func (s *attestationState) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstSeen = false
	s.verified = nil
	s.pinnedChainPEM = nil
}

// attestationError marks an error as an attestation failure without changing
// its message, so errors from a custom Verifier are classified too.
type attestationError struct{ err error }

func (e attestationError) Error() string { return e.err.Error() }
func (e attestationError) Unwrap() error { return e.err }

// failAttestation wraps err as an attestationError, except context
// cancellation, which means shutdown rather than a verification failure.
func failAttestation(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return attestationError{err}
}

// isAttestationFailure reports whether err is an attestation failure, which
// requires terminating the connection.
func isAttestationFailure(err error) bool {
	var ae attestationError
	return errors.As(err, &ae)
}

// ProcessEnvelope validates the trust chain when one is delivered (first
// message or rotation), verifies the signature, and returns the inner
// ServerToAgent bytes. On error the caller MUST terminate the connection.
func (s *attestationState) ProcessEnvelope(ctx context.Context, envelope *protobufs.SignedServerToAgent) ([]byte, error) {
	payload, err := s.processEnvelope(ctx, envelope)
	if err != nil {
		return nil, failAttestation(err)
	}
	return payload, nil
}

func (s *attestationState) processEnvelope(ctx context.Context, envelope *protobufs.SignedServerToAgent) ([]byte, error) {
	if envelope == nil {
		return nil, fmt.Errorf("%w: nil envelope", ErrMalformedEnvelope)
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
			return nil, fmt.Errorf("%w: %v", ErrMalformedTrustChain, err)
		}

		if s.enroller != nil {
			if err := s.enroll(ctx, chainResp.TofuTrustAnchor, chainDER, envelope); err != nil {
				return nil, err
			}
		}

		verified, err := s.verifier.ValidateChain(ctx, chainDER, time.Now(), s.serverName)
		if err != nil {
			return nil, fmt.Errorf("client: validate trust chain: %w", err)
		}
		s.verified = verified
		s.pinnedChainPEM = chainResp.CertificateChain
		s.firstSeen = true
	} else if !s.firstSeen {
		return nil, ErrMissingTrustChain
	}

	if len(envelope.Signature) == 0 {
		return nil, ErrMissingSignature
	}
	if err := s.verifier.Verify(ctx, envelope.Payload, envelope.Signature, s.verified); err != nil {
		return nil, fmt.Errorf("client: verify signature: %w", err)
	}
	s.verifiedOnce = true
	return envelope.Payload, nil
}

// enroll performs TOFU enrollment. The anchor is persisted only after the
// chain and this envelope's signature verify under it. Caller holds s.mu.
func (s *attestationState) enroll(ctx context.Context, anchorPEM []byte, chainDER [][]byte, envelope *protobufs.SignedServerToAgent) error {
	if len(anchorPEM) == 0 {
		return ErrTOFUAnchorMissing
	}
	if len(envelope.Signature) == 0 {
		return ErrMissingSignature
	}
	candidate, err := signing.VerifierFromPEM(anchorPEM)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTOFUEnrollment, err)
	}
	verified, err := candidate.ValidateChain(ctx, chainDER, time.Now(), s.serverName)
	if err != nil {
		return fmt.Errorf("%w: validate trust chain against offered anchor: %w", ErrTOFUEnrollment, err)
	}
	if err := candidate.Verify(ctx, envelope.Payload, envelope.Signature, verified); err != nil {
		return fmt.Errorf("%w: verify signature against offered anchor: %w", ErrTOFUEnrollment, err)
	}
	v, err := s.enroller.Enroll(anchorPEM)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTOFUEnrollment, err)
	}
	s.verifier = v
	s.enroller = nil
	return nil
}

// parsePEMChain returns the DER bytes of each CERTIFICATE block, in order.
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

// unwrapServerToAgent decodes rawProto (transport framing already removed)
// into msg, verifying it first when state is non-nil.
func unwrapServerToAgent(ctx context.Context, state *attestationState, rawProto []byte, msg *protobufs.ServerToAgent) error {
	if state == nil {
		return proto.Unmarshal(rawProto, msg)
	}
	var envelope protobufs.SignedServerToAgent
	if err := proto.Unmarshal(rawProto, &envelope); err != nil {
		return failAttestation(fmt.Errorf("%w: %v", ErrMalformedEnvelope, err))
	}

	// Envelope fields are 14-16, so a plain ServerToAgent decodes as an
	// empty envelope: treat it as unsigned, accepted only if it is a
	// heartbeat. A partial envelope falls through and is rejected below.
	if len(envelope.Payload) == 0 && len(envelope.Signature) == 0 && envelope.TrustChainResponse == nil {
		if err := proto.Unmarshal(rawProto, msg); err != nil {
			return fmt.Errorf("client: decode unsigned ServerToAgent: %w", err)
		}
		if protobufs.IsHeartbeatServerToAgent(msg) {
			return nil
		}
		proto.Reset(msg)
		return failAttestation(ErrUnsignedNonHeartbeat)
	}

	payload, err := state.ProcessEnvelope(ctx, &envelope)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(payload, msg); err != nil {
		return fmt.Errorf("client: decode inner ServerToAgent: %w", err)
	}
	if proto.Equal(msg, &protobufs.ServerToAgent{}) {
		return failAttestation(ErrEmptyInnerServerToAgent)
	}
	return nil
}
