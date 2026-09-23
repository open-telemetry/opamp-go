package server

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

// connectionSigningState holds the per-connection state needed to wrap
// outbound ServerToAgent messages in SignedServerToAgent envelopes
// when payload trust verification has been negotiated.
//
// The chain is delivered on the first message and re-delivered whenever
// the signer's chain changes (rotation). The root/trust anchor never
// changes; only the chain to it does.
type connectionSigningState struct {
	signer        signing.Signer
	tofuAnchorPEM []byte // non-empty iff this connection is a TOFU enrollment
	tofuError     string // non-empty when TOFU requested but anchor unavailable

	mu           sync.Mutex
	lastChainPEM []byte // PEM of the chain last delivered; nil until first delivery
	firstSent    bool
}

// newConnectionSigningState constructs the per-connection state. There is no
// pre-flight signer check: the chain arrives per message in SignResult, and a
// bad signer surfaces from the first Sign in signOutgoing. When tofu is true
// the signer must also implement TrustAnchorProvider, whose root CA is fetched
// and stored for the first TrustChainResponse.
func newConnectionSigningState(ctx context.Context, signer signing.Signer, tofu bool) (*connectionSigningState, error) {
	if signer == nil {
		return nil, fmt.Errorf("server: nil signer")
	}
	state := &connectionSigningState{signer: signer}
	if tofu {
		tap, ok := signer.(signing.TrustAnchorProvider)
		if !ok {
			state.tofuError = "server cannot provide TOFU trust anchor: signer does not implement TrustAnchorProvider"
		} else {
			anchorPEM, err := tap.TrustAnchorPEM(ctx)
			if err != nil {
				state.tofuError = fmt.Sprintf("server cannot provide TOFU trust anchor: %v", err)
			} else {
				state.tofuAnchorPEM = anchorPEM
			}
		}
	}
	return state, nil
}

// signOutgoing produces a SignedServerToAgent envelope wrapping msg. It
// attaches trust_chain_response on the first message and again whenever the
// signing chain changes, so the Agent can re-validate and re-pin without
// dropping the connection.
func (s *connectionSigningState) signOutgoing(ctx context.Context, msg *protobufs.ServerToAgent) (*protobufs.SignedServerToAgent, error) {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("server: marshal inner ServerToAgent: %w", err)
	}
	// Sign returns the exact bytes it signed, the signature, and the anchoring
	// chain in one SignResult (see signing.SignResult for why all three come
	// from one call). We MUST transmit res.Payload, not our marshalling above:
	// a signer that re-marshals server-side yields different bytes, and the
	// Agent verifies over what it receives.
	res, err := s.signer.Sign(ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("server: sign payload: %w", err)
	}
	env := &protobufs.SignedServerToAgent{
		Payload:   res.Payload,
		Signature: res.Signature,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tofuError != "" {
		if !s.firstSent {
			s.firstSent = true
			env.TrustChainResponse = &protobufs.TrustChainResponse{ErrorMessage: s.tofuError}
		}
		return env, nil
	}

	var pemChain []byte
	for _, der := range res.ChainDER {
		pemChain = append(pemChain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	if !s.firstSent || !bytes.Equal(pemChain, s.lastChainPEM) {
		env.TrustChainResponse = &protobufs.TrustChainResponse{CertificateChain: pemChain}
		if !s.firstSent {
			env.TrustChainResponse.TofuTrustAnchor = s.tofuAnchorPEM
		}
		s.lastChainPEM = pemChain
		s.firstSent = true
	}
	return env, nil
}

// agentRequiresAttestation reports whether the supplied
// AgentToServer.capabilities bitmask requests payload trust
// verification.
func agentRequiresAttestation(capabilities uint64) bool {
	return capabilities&uint64(protobufs.AgentCapabilities_AgentCapabilities_RequiresPayloadTrustVerification) != 0
}

// agentRequestsTOFU reports whether the agent is requesting TOFU
// enrollment (no pre-configured trust anchor; needs the root CA).
func agentRequestsTOFU(capabilities uint64) bool {
	return capabilities&uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsPayloadTrustAnchorTOFU) != 0
}

// addOffersAttestationBit returns capabilities with the
// ServerCapabilities_OffersPayloadTrustVerification bit set. It is a
// no-op if the bit is already set.
func addOffersAttestationBit(capabilities uint64) uint64 {
	return capabilities | uint64(protobufs.ServerCapabilities_ServerCapabilities_OffersPayloadTrustVerification)
}
