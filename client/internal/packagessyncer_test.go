package internal

import (
	"crypto/tls"
	"net/http"
	"sync"
	"testing"
	"time"

	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackageSyncer_New(t *testing.T) {
	tests := []struct {
		name      string
		tlsConfig *tls.Config
	}{
		{
			name:      "without TLS",
			tlsConfig: nil,
		},
		{
			name:      "with TLS",
			tlsConfig: &tls.Config{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &sharedinternal.NopLogger{}
			syncer := NewPackagesSyncer(
				logger,
				&protobufs.PackagesAvailable{},
				NewHTTPSender(logger),
				tt.tlsConfig,
				&ClientSyncedState{},
				NewInMemPackagesStore(),
				&sync.Mutex{},
				time.Minute,
			)

			require.NotNil(t, syncer)
			tr, ok := syncer.httpClient.Transport.(*http.Transport)
			require.True(t, ok, "Transport should be of type *http.Transport")
			if tt.tlsConfig != nil {
				assert.Equal(t, tr.TLSClientConfig, tt.tlsConfig)
			}
		})
	}
}
