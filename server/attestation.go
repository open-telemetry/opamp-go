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

// connectionSigningState signs outbound messages on an attested connection.
// The chain is sent with the first message and again whenever it changes
// (rotation).
type connectionSigningState struct {
	signer        signing.Signer
	tofuAnchorPEM []byte // non-empty iff this connection is a TOFU enrollment
	tofuError     string // non-empty when TOFU requested but anchor unavailable

	mu           sync.Mutex
	lastChainPEM []byte // PEM of the chain last delivered; nil until first delivery
	firstSent    bool
}

// newConnectionSigningState returns state for a non-nil signer. For a TOFU
// Agent it fetches the root CA to offer; if the signer cannot provide one,
// the first message reports that in trust_chain_response.error_message.
func newConnectionSigningState(ctx context.Context, signer signing.Signer, tofu bool) *connectionSigningState {
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
	return state
}

// signOutgoing wraps msg in a signed envelope, attaching trust_chain_response
// on the first message and whenever the chain changes.
func (s *connectionSigningState) signOutgoing(ctx context.Context, msg *protobufs.ServerToAgent) (*protobufs.SignedServerToAgent, error) {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("server: marshal inner ServerToAgent: %w", err)
	}
	// Transmit res.Payload, not payload: a signer may re-marshal.
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

// agentRequiresAttestation reports whether the Agent requires attestation.
func agentRequiresAttestation(capabilities uint64) bool {
	return capabilities&uint64(protobufs.AgentCapabilities_AgentCapabilities_RequiresPayloadTrustVerification) != 0
}

// agentRequestsTOFU reports whether the Agent accepts a TOFU trust anchor.
func agentRequestsTOFU(capabilities uint64) bool {
	return capabilities&uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsPayloadTrustAnchorTOFU) != 0
}

// addOffersAttestationBit sets OffersPayloadTrustVerification in capabilities.
func addOffersAttestationBit(capabilities uint64) uint64 {
	return capabilities | uint64(protobufs.ServerCapabilities_ServerCapabilities_OffersPayloadTrustVerification)
}
