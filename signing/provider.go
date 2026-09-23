package signing

// PayloadTrustProvider is the single client-side entry point for opting in to
// payload trust verification. An operator sets one on the client StartSettings
// to enable attestation; leaving it nil (the default) keeps the standard OpAMP
// wire path with no verification.
//
// The core capability is Verifier: return the [Verifier] that validates the
// server's delivered trust chain and per-message signatures. Additional modes
// are expressed as optional interfaces a provider MAY also satisfy — currently
// [TOFUEnroller] for Trust On First Use enrollment — which the client detects
// via a type assertion. New modes can be added the same way without changing
// this interface or the StartSettings surface.
//
// Use [FixedAnchor] for a fixed, pre-configured trust anchor and [TOFUAnchor]
// for TOFU enrollment.
type PayloadTrustProvider interface {
	// Verifier returns the Verifier the Agent uses to validate the server's
	// trust chain and per-message signatures.
	//
	// It returns (nil, nil) when the provider has no trust anchor configured
	// yet: the TOFU enrollment case, where the anchor is bootstrapped from the
	// first TrustChainResponse. A provider that returns no Verifier MUST also
	// implement TOFUEnroller, otherwise attestation cannot proceed. A non-nil
	// error aborts client startup.
	Verifier() (Verifier, error)
}

// TOFUEnroller is an optional interface a [PayloadTrustProvider] MAY satisfy to
// support Trust On First Use (TOFU) enrollment. When a provider's Verifier
// returns no verifier and the provider also implements TOFUEnroller, the client
// advertises AcceptsPayloadTrustAnchorTOFU, accepts the root CA delivered in
// the first TrustChainResponse.tofu_trust_anchor, persists it via Enroll, and
// uses the returned Verifier for the remainder of the session.
type TOFUEnroller interface {
	// Enroll persists the PEM-encoded trust anchor acquired on first
	// connection and returns the Verifier built from it.
	//
	// Implementations MUST be idempotent: if an anchor is already stored,
	// Enroll MUST NOT overwrite it. This prevents a reconnecting agent from
	// replacing a valid anchor with a potentially attacker-supplied one.
	Enroll(anchorPEM []byte) (Verifier, error)
}

// FixedAnchor returns a PayloadTrustProvider backed by a fixed, pre-configured
// Verifier — for example one built with [VerifierFromFile] from a CA bundle.
// The returned provider does not implement [TOFUEnroller]; the trust anchor is
// established entirely from v.
func FixedAnchor(v Verifier) PayloadTrustProvider {
	return fixedAnchorProvider{v: v}
}

// TOFUAnchor returns a PayloadTrustProvider that performs Trust On First Use
// enrollment backed by store. On the first startup where store holds no anchor,
// the client enrolls the root CA delivered by the server and persists it via
// store; on later startups the stored anchor is loaded and used directly.
//
// WARNING: TOFU provides no security on the first connection; a compromised
// distribution server can install an attacker-controlled trust anchor. Use only
// for environments where the first connection is considered sufficiently
// trusted, and provide a store backed by persistent storage — agents in
// stateless environments without a persistent volume will repeat enrollment on
// every restart.
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

// tofuAnchorProvider is a PayloadTrustProvider that resolves its verifier from
// a TOFUStore, enrolling the anchor on first use. It satisfies TOFUEnroller.
type tofuAnchorProvider struct {
	store TOFUStore
}

var _ TOFUEnroller = tofuAnchorProvider{}

// Verifier loads a previously-enrolled anchor from the store. It returns
// (nil, nil) when no anchor has been stored yet, signalling that the client
// should enter TOFU enrollment mode (see Enroll).
func (p tofuAnchorProvider) Verifier() (Verifier, error) {
	anchorPEM, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	if len(anchorPEM) == 0 {
		return nil, nil
	}
	v, err := VerifierFromPEM(anchorPEM)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Enroll persists the anchor acquired on first connection and returns a
// Verifier built from it. Persistence is idempotent via the store's write-once
// Save contract, so a concurrent or repeat enrollment never overwrites a stored
// anchor.
func (p tofuAnchorProvider) Enroll(anchorPEM []byte) (Verifier, error) {
	v, err := VerifierFromPEM(anchorPEM)
	if err != nil {
		return nil, err
	}
	if err := p.store.Save(anchorPEM); err != nil {
		return nil, err
	}
	return v, nil
}
