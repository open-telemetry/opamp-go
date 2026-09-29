package internal

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

const requiresAttestation = protobufs.AgentCapabilities_AgentCapabilities_RequiresPayloadTrustVerification

// providerFunc is a PayloadTrustProvider without TOFU support.
type providerFunc func() (signing.Verifier, error)

func (f providerFunc) Verifier() (signing.Verifier, error) { return f() }

func newTestClientCommon(t *testing.T, caps protobufs.AgentCapabilities) *ClientCommon {
	t.Helper()
	c := NewClientCommon(&sharedinternal.NopLogger{}, NewHTTPSender(&sharedinternal.NopLogger{}))
	require.NoError(t, c.SetAgentDescription(&protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{{
			Key:   "service.name",
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "test"}},
		}},
	}))
	require.NoError(t, c.SetCapabilities(&caps))
	return &c
}

func startSettings(p signing.PayloadTrustProvider) types.StartSettings {
	return types.StartSettings{PayloadTrustProvider: p, InstanceUid: types.InstanceUid{1}}
}

func TestPrepareStartPayloadTrust(t *testing.T) {
	pki := newTestPKI(t)
	ctx := context.Background()

	t.Run("fixed anchor", func(t *testing.T) {
		c := newTestClientCommon(t, requiresAttestation)
		require.NoError(t, c.PrepareStart(ctx, startSettings(signing.FixedAnchor(pki.verifier(t)))))
		v, e := c.PayloadTrust()
		assert.NotNil(t, v)
		assert.Nil(t, e)
		assert.True(t, c.PayloadTrustEnabled())
	})

	t.Run("TOFU with empty store", func(t *testing.T) {
		_, enroller := newTOFUStore(t)
		c := newTestClientCommon(t, requiresAttestation)
		require.NoError(t, c.PrepareStart(ctx, startSettings(enroller.(signing.PayloadTrustProvider))))
		v, e := c.PayloadTrust()
		assert.Nil(t, v)
		assert.IsType(t, &promotingEnroller{}, e)
	})

	t.Run("disabled", func(t *testing.T) {
		c := newTestClientCommon(t, protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus)
		require.NoError(t, c.PrepareStart(ctx, startSettings(nil)))
		assert.False(t, c.PayloadTrustEnabled())
	})

	errs := []struct {
		name     string
		caps     protobufs.AgentCapabilities
		provider signing.PayloadTrustProvider
		want     error
	}{
		{"verifier error", requiresAttestation, providerFunc(func() (signing.Verifier, error) { return nil, errors.New("boom") }), ErrPayloadVerifierInit},
		{"no anchor and no TOFU", requiresAttestation, providerFunc(func() (signing.Verifier, error) { return nil, nil }), ErrPayloadTrustProviderNoAnchor},
		{"capability without provider", requiresAttestation, nil, ErrPayloadVerifierMissing},
		{"provider without capability", protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus, signing.FixedAnchor(pki.verifier(t)), ErrPayloadVerifierWithoutCapability},
	}
	for _, tc := range errs {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClientCommon(t, tc.caps)
			require.ErrorIs(t, c.PrepareStart(ctx, startSettings(tc.provider)), tc.want)
		})
	}
}

func TestSetCapabilitiesValidatesPayloadTrustWhenStarted(t *testing.T) {
	pki := newTestPKI(t)
	c := newTestClientCommon(t, requiresAttestation)
	require.NoError(t, c.PrepareStart(context.Background(), startSettings(signing.FixedAnchor(pki.verifier(t)))))
	c.isStarted = true

	caps := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus
	require.ErrorIs(t, c.SetCapabilities(&caps), ErrPayloadVerifierWithoutCapability)
	caps = requiresAttestation
	require.NoError(t, c.SetCapabilities(&caps))
}

func TestPromotingEnroller(t *testing.T) {
	pki := newTestPKI(t)

	t.Run("success promotes the verifier", func(t *testing.T) {
		_, enroller := newTOFUStore(t)
		c := newTestClientCommon(t, requiresAttestation)
		c.PayloadTOFUEnroller = &promotingEnroller{inner: enroller, common: c}

		var wg sync.WaitGroup
		stop := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						_, _ = c.PayloadTrust()
						_ = c.PayloadTrustEnabled()
					}
				}
			}()
		}
		v, err := c.PayloadTOFUEnroller.Enroll(pki.caPEM)
		close(stop)
		wg.Wait()

		require.NoError(t, err)
		gotV, gotE := c.PayloadTrust()
		assert.Same(t, v, gotV)
		assert.Nil(t, gotE)
	})

	t.Run("failure leaves state untouched", func(t *testing.T) {
		boom := errors.New("boom")
		c := newTestClientCommon(t, requiresAttestation)
		pe := &promotingEnroller{inner: enrollerFunc(func([]byte) (signing.Verifier, error) { return nil, boom }), common: c}
		c.PayloadTOFUEnroller = pe

		_, err := pe.Enroll(pki.caPEM)
		require.ErrorIs(t, err, boom)
		v, e := c.PayloadTrust()
		assert.Nil(t, v)
		assert.Same(t, pe, e)
	})
}
