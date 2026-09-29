package internal

import (
	"context"
	"encoding/pem"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

func TestProcessEnvelopeFirstMessageAndPinnedLeaf(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	state := newAttestationState(pki.verifier(t), testHost, nil)
	ctx := context.Background()

	env := signedMsg(t, s, testMsg, true)
	got, err := state.ProcessEnvelope(ctx, env)
	require.NoError(t, err)
	assert.Equal(t, env.Payload, got)
	assert.True(t, state.HasVerified())

	// Later messages need no chain: they verify against the pinned leaf.
	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s, testMsg, false))
	require.NoError(t, err)
}

func TestProcessEnvelopeFailures(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	good := signedMsg(t, s, testMsg, true)
	other := newTestPKI(t)

	tamper := func(env *protobufs.SignedServerToAgent) *protobufs.SignedServerToAgent {
		c := &protobufs.SignedServerToAgent{Payload: env.Payload, TrustChainResponse: env.TrustChainResponse}
		c.Signature = append([]byte{}, env.Signature...)
		c.Signature[len(c.Signature)-1] ^= 0xff
		return c
	}

	tests := []struct {
		name     string
		verifier signing.Verifier
		host     string
		env      *protobufs.SignedServerToAgent
		want     error
	}{
		{"nil envelope", pki.verifier(t), testHost, nil, ErrMalformedEnvelope},
		{"empty payload", pki.verifier(t), testHost, &protobufs.SignedServerToAgent{Signature: good.Signature}, ErrMissingPayload},
		{"no chain on first message", pki.verifier(t), testHost, signedMsg(t, s, testMsg, false), ErrMissingTrustChain},
		{"server reported chain error", pki.verifier(t), testHost, &protobufs.SignedServerToAgent{
			Payload: good.Payload, Signature: good.Signature,
			TrustChainResponse: &protobufs.TrustChainResponse{ErrorMessage: "no chain"},
		}, ErrTrustChainErrorReported},
		{"malformed chain PEM", pki.verifier(t), testHost, &protobufs.SignedServerToAgent{
			Payload: good.Payload, Signature: good.Signature,
			TrustChainResponse: &protobufs.TrustChainResponse{CertificateChain: []byte("junk")},
		}, ErrMalformedTrustChain},
		{"missing signature", pki.verifier(t), testHost, &protobufs.SignedServerToAgent{
			Payload: good.Payload, TrustChainResponse: good.TrustChainResponse,
		}, ErrMissingSignature},
		{"bad signature", pki.verifier(t), testHost, tamper(good), signing.ErrSignatureMismatch},
		{"SAN mismatch", pki.verifier(t), "other.test", good, signing.ErrHostnameMismatch},
		{"empty server name", pki.verifier(t), "", good, signing.ErrServerNameRequired},
		{"untrusted CA", other.verifier(t), testHost, good, signing.ErrChainValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := newAttestationState(tc.verifier, tc.host, nil)
			_, err := state.ProcessEnvelope(context.Background(), tc.env)
			require.ErrorIs(t, err, tc.want)
			assert.True(t, isAttestationFailure(err))
			assert.False(t, state.HasVerified())
		})
	}
}

func TestProcessEnvelopeCustomVerifierErrors(t *testing.T) {
	pki := newTestPKI(t)
	env := signedMsg(t, pki.signer(t), testMsg, true)

	boom := errors.New("boom")
	_, err := newAttestationState(errVerifier{boom}, testHost, nil).ProcessEnvelope(context.Background(), env)
	require.ErrorIs(t, err, boom)
	assert.True(t, isAttestationFailure(err), "arbitrary verifier errors are attestation failures")

	_, err = newAttestationState(errVerifier{context.Canceled}, testHost, nil).ProcessEnvelope(context.Background(), env)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, isAttestationFailure(err), "cancellation is not an attestation failure")

	assert.False(t, isAttestationFailure(nil))
	assert.False(t, isAttestationFailure(boom))
}

func TestProcessEnvelopeChainRedeliveryAndRotation(t *testing.T) {
	pki := newTestPKI(t)
	s1, s2 := pki.signer(t), pki.signer(t)
	v := &countingVerifier{Verifier: pki.verifier(t)}
	state := newAttestationState(v, testHost, nil)
	ctx := context.Background()

	_, err := state.ProcessEnvelope(ctx, signedMsg(t, s1, testMsg, true))
	require.NoError(t, err)
	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s1, testMsg, true))
	require.NoError(t, err)
	assert.Equal(t, 1, v.validations, "an unchanged chain is not re-validated")

	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s2, testMsg, true))
	require.NoError(t, err)
	assert.Equal(t, 2, v.validations, "a rotated chain is re-validated")

	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s2, testMsg, false))
	require.NoError(t, err)
	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s1, testMsg, false))
	require.ErrorIs(t, err, signing.ErrSignatureMismatch, "the rotated leaf is now pinned")
}

func TestProcessEnvelopeReset(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	state := newAttestationState(pki.verifier(t), testHost, nil)
	ctx := context.Background()

	_, err := state.ProcessEnvelope(ctx, signedMsg(t, s, testMsg, true))
	require.NoError(t, err)
	state.Reset()

	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s, testMsg, false))
	require.ErrorIs(t, err, ErrMissingTrustChain)
	assert.True(t, state.HasVerified(), "Reset keeps verifiedOnce")

	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s, testMsg, true))
	require.NoError(t, err)
}

func newTOFUStore(t *testing.T) (*signing.FileTOFUStore, signing.TOFUEnroller) {
	t.Helper()
	store := signing.NewFileTOFUStore(filepath.Join(t.TempDir(), "anchor.pem"))
	return store, signing.TOFUAnchor(store).(signing.TOFUEnroller)
}

func tofuEnvelope(t *testing.T, s signing.Signer, anchor []byte) *protobufs.SignedServerToAgent {
	env := signedMsg(t, s, testMsg, true)
	env.TrustChainResponse.TofuTrustAnchor = anchor
	return env
}

func requireNotStored(t *testing.T, store *signing.FileTOFUStore) {
	t.Helper()
	got, err := store.Load()
	require.NoError(t, err)
	assert.Nil(t, got, "anchor must not be persisted")
}

func TestProcessEnvelopeTOFUEnrollment(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	store, enroller := newTOFUStore(t)
	state := newAttestationState(nil, testHost, enroller)
	ctx := context.Background()

	_, err := state.ProcessEnvelope(ctx, tofuEnvelope(t, s, pki.caPEM))
	require.NoError(t, err)
	stored, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, pki.caPEM, stored)
	assert.Nil(t, state.enroller)
	require.NotNil(t, state.verifier)

	// Enrollment is complete: later messages verify without an anchor.
	_, err = state.ProcessEnvelope(ctx, signedMsg(t, s, testMsg, false))
	require.NoError(t, err)
}

func TestProcessEnvelopeTOFURejections(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	other := newTestPKI(t)

	badSig := tofuEnvelope(t, s, pki.caPEM)
	badSig.Signature[len(badSig.Signature)-1] ^= 0xff
	noSig := tofuEnvelope(t, s, pki.caPEM)
	noSig.Signature = nil

	tests := []struct {
		name  string
		env   *protobufs.SignedServerToAgent
		wants []error
	}{
		{"missing anchor", tofuEnvelope(t, s, nil), []error{ErrTOFUAnchorMissing}},
		{"garbage anchor", tofuEnvelope(t, s, []byte("junk")), []error{ErrTOFUEnrollment}},
		{"chain not under offered anchor", tofuEnvelope(t, s, other.caPEM), []error{ErrTOFUEnrollment, signing.ErrChainValidation}},
		{"signature invalid under offered anchor", badSig, []error{ErrTOFUEnrollment, signing.ErrSignatureMismatch}},
		{"missing signature", noSig, []error{ErrMissingSignature}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, enroller := newTOFUStore(t)
			state := newAttestationState(nil, testHost, enroller)
			_, err := state.ProcessEnvelope(context.Background(), tc.env)
			for _, want := range tc.wants {
				require.ErrorIs(t, err, want)
			}
			assert.True(t, isAttestationFailure(err))
			assert.NotNil(t, state.enroller, "enrollment remains pending")
			requireNotStored(t, store)
		})
	}
}

func TestProcessEnvelopeTOFUEnrollerError(t *testing.T) {
	pki := newTestPKI(t)
	boom := errors.New("store unavailable")
	state := newAttestationState(nil, testHost, enrollerFunc(func([]byte) (signing.Verifier, error) { return nil, boom }))

	_, err := state.ProcessEnvelope(context.Background(), tofuEnvelope(t, pki.signer(t), pki.caPEM))
	require.ErrorIs(t, err, ErrTOFUEnrollment)
	require.ErrorIs(t, err, boom)
}

func TestProcessEnvelopeTOFUStoredAnchorWins(t *testing.T) {
	pkiA, pkiB := newTestPKI(t), newTestPKI(t)
	store, enroller := newTOFUStore(t)
	require.NoError(t, store.Save(pkiA.caPEM))

	state := newAttestationState(nil, testHost, enroller)
	_, err := state.ProcessEnvelope(context.Background(), tofuEnvelope(t, pkiB.signer(t), pkiB.caPEM))
	require.ErrorIs(t, err, signing.ErrChainValidation, "the stored anchor, not the offered one, is trusted")

	stored, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, pkiA.caPEM, stored)
}

func TestUnwrapServerToAgentAttested(t *testing.T) {
	pki := newTestPKI(t)
	s := pki.signer(t)
	ctx := context.Background()

	t.Run("signed message", func(t *testing.T) {
		state := newAttestationState(pki.verifier(t), testHost, nil)
		var msg protobufs.ServerToAgent
		require.NoError(t, unwrapServerToAgent(ctx, state, mustMarshal(t, signedMsg(t, s, testMsg, true)), &msg))
		assert.Equal(t, testMsg.InstanceUid, msg.InstanceUid)
	})

	t.Run("malformed envelope", func(t *testing.T) {
		state := newAttestationState(pki.verifier(t), testHost, nil)
		var msg protobufs.ServerToAgent
		err := unwrapServerToAgent(ctx, state, []byte{0xff}, &msg)
		require.ErrorIs(t, err, ErrMalformedEnvelope)
		assert.True(t, isAttestationFailure(err))
	})

	t.Run("empty inner message", func(t *testing.T) {
		state := newAttestationState(pki.verifier(t), testHost, nil)
		// flags=0 encoded explicitly: a non-empty payload that decodes to an empty message.
		env := signedEnvelope(t, s, []byte{0x30, 0x00}, true)
		var msg protobufs.ServerToAgent
		err := unwrapServerToAgent(ctx, state, mustMarshal(t, env), &msg)
		require.ErrorIs(t, err, ErrEmptyInnerServerToAgent)
		assert.True(t, isAttestationFailure(err))
	})

	t.Run("undecodable inner message", func(t *testing.T) {
		state := newAttestationState(pki.verifier(t), testHost, nil)
		env := signedEnvelope(t, s, []byte{0xff}, true)
		var msg protobufs.ServerToAgent
		err := unwrapServerToAgent(ctx, state, mustMarshal(t, env), &msg)
		require.Error(t, err)
		assert.False(t, isAttestationFailure(err), "a validly signed but undecodable payload is not an attestation failure")
	})
}

func TestProcessEnvelopeIgnoresNonCertificatePEMBlocks(t *testing.T) {
	pki := newTestPKI(t)
	env := signedMsg(t, pki.signer(t), testMsg, true)
	extra := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})
	env.TrustChainResponse.CertificateChain = append(extra, env.TrustChainResponse.CertificateChain...)

	_, err := newAttestationState(pki.verifier(t), testHost, nil).ProcessEnvelope(context.Background(), env)
	require.NoError(t, err)
}

func TestUnwrapServerToAgentUndecodableUnsigned(t *testing.T) {
	// error_response.error_message holding invalid UTF-8: an empty envelope,
	// but not a valid ServerToAgent.
	raw := []byte{0x12, 0x03, 0x12, 0x01, 0xff}
	var msg protobufs.ServerToAgent
	err := unwrapServerToAgent(context.Background(), newAttestationState(nil, testHost, nil), raw, &msg)
	require.Error(t, err)
	assert.False(t, isAttestationFailure(err))
}
