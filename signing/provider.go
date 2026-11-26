package signing

import "fmt"

// PayloadTrustProvider opts a client in to payload trust verification via
// StartSettings. Optional modes are separate interfaces the client detects by
// type assertion (currently [TOFUEnroller]), so modes can be added without
// changing this interface. Use [FixedAnchor] or [TOFUAnchor].
type PayloadTrustProvider interface {
	// Verifier returns the Verifier for the server's trust chain and
	// signatures, or (nil, nil) if no anchor is configured yet, in which
	// case the provider MUST implement TOFUEnroller. An error aborts
	// client startup.
	Verifier() (Verifier, error)
}

// TOFUEnroller is an optional [PayloadTrustProvider] interface for Trust On
// First Use enrollment. When Verifier returns nil, the client takes the root
// CA from the first trust_chain_response.tofu_trust_anchor, checks that it
// is a valid anchor (see [TOFUAnchorFromPEM]) and that the delivered chain
// and signature verify under it, then calls Enroll and uses the returned
// Verifier for all later connections. The client advertises
// AcceptsPayloadTrustAnchorTOFU only while enrollment is pending.
type TOFUEnroller interface {
	// Enroll persists anchorPEM, which the client passes as the single
	// validated certificate re-encoded as PEM, and returns a Verifier for
	// the stored anchor. It MUST NOT overwrite an anchor that is already
	// stored, and the returned Verifier MUST reflect the stored anchor, so
	// a repeat enrollment cannot substitute a different one.
	Enroll(anchorPEM []byte) (Verifier, error)
}

// FixedAnchor returns a PayloadTrustProvider backed by a pre-configured
// Verifier, for example one from [VerifierFromFile].
func FixedAnchor(v Verifier) PayloadTrustProvider {
	return fixedAnchorProvider{v: v}
}

// TOFUAnchor returns a PayloadTrustProvider that enrolls the server's root CA
// on first use and persists it in store; later startups load it from store.
//
// WARNING: TOFU provides no security on the first connection, where a
// compromised server can install its own anchor. Use it only where that
// connection is trusted, with persistent storage; without it, every restart
// enrolls again.
func TOFUAnchor(store TOFUStore) PayloadTrustProvider {
	return tofuAnchorProvider{store: store}
}

// fixedAnchorProvider is a PayloadTrustProvider with a pre-configured verifier.
type fixedAnchorProvider struct {
	v Verifier
}

func (p fixedAnchorProvider) Verifier() (Verifier, error) {
	return p.v, nil
}

// tofuAnchorProvider resolves its verifier from a TOFUStore and implements
// TOFUEnroller.
type tofuAnchorProvider struct {
	store TOFUStore
}

var _ TOFUEnroller = tofuAnchorProvider{}

// Verifier returns a verifier for the stored anchor, or (nil, nil) if none
// has been enrolled yet.
func (p tofuAnchorProvider) Verifier() (Verifier, error) {
	anchorPEM, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	if len(anchorPEM) == 0 {
		return nil, nil
	}
	return storedVerifier(anchorPEM)
}

// Enroll validates anchorPEM (see [TOFUAnchorFromPEM]), saves the certificate
// it holds (a no-op if an anchor is already stored), and returns a verifier
// for the anchor actually stored.
func (p tofuAnchorProvider) Enroll(anchorPEM []byte) (Verifier, error) {
	_, anchor, err := TOFUAnchorFromPEM(anchorPEM)
	if err != nil {
		return nil, err
	}
	if err := p.store.Save(anchor); err != nil {
		return nil, err
	}
	stored, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	return storedVerifier(stored)
}

// storedVerifier returns a verifier for an anchor read back from the store,
// which must meet the same rules as an offered one.
func storedVerifier(anchorPEM []byte) (Verifier, error) {
	v, _, err := TOFUAnchorFromPEM(anchorPEM)
	if err != nil {
		return nil, fmt.Errorf("signing: stored TOFU trust anchor is unusable; delete it to re-enroll: %w", err)
	}
	return v, nil
}
