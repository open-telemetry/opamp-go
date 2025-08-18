package internal

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPackagesSyncer(t *testing.T) {
	tests := []struct {
		name          string
		clientFactory func(context.Context, *protobufs.DownloadableFile) (*http.Client, error)
		err           string
	}{
		{
			name:          "nil client factory",
			clientFactory: nil,
			err:           "httpClientFactory must not be nil",
		},
		{
			name: "non-nil client factory",
			clientFactory: func(context.Context, *protobufs.DownloadableFile) (*http.Client, error) {
				return &http.Client{}, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewPackagesSyncer(
				nil,
				&protobufs.PackagesAvailable{},
				nil,
				&ClientSyncedState{},
				&InMemPackagesStore{},
				&sync.Mutex{},
				time.Second,
				tt.clientFactory,
			)
			if tt.err != "" {
				assert.EqualError(t, err, tt.err)
				assert.Nil(t, s)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, s)
			}
		})
	}
}

func TestPackageSyncerSync(t *testing.T) {
	tests := []struct {
		name                 string
		testFileContent      []byte
		getPackagesAvailable func(serverURL string, testFileContentHash []byte) *protobufs.PackagesAvailable
		err                  string
	}{
		{
			name:            "package available",
			testFileContent: []byte("test package content for testing"),
			getPackagesAvailable: func(serverURL string, testFileContentHash []byte) *protobufs.PackagesAvailable {
				return &protobufs.PackagesAvailable{
					Packages: map[string]*protobufs.PackageAvailable{
						"": {
							Type:    protobufs.PackageType_PackageType_TopLevel,
							Version: "1.0.0",
							Hash:    testFileContentHash[:],
							File: &protobufs.DownloadableFile{
								DownloadUrl: serverURL,
								ContentHash: testFileContentHash[:],
								Signature:   []byte{},
							},
						},
					},
					AllPackagesHash: testFileContentHash[:],
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test server
			_, serverURL := createTestHTTPServer(t, tt.testFileContent)

			// Setup packages available
			testFileContentHash := sha256.Sum256(tt.testFileContent)
			packagesAvailable := tt.getPackagesAvailable(serverURL, testFileContentHash[:])

			// Setup packages store with initial state
			store := NewInMemPackagesStore()
			store.SetAllPackagesHash([]byte{})
			store.SetPackageState("", types.PackageState{
				Exists:  true,
				Type:    protobufs.PackageType_PackageType_TopLevel,
				Hash:    []byte{},
				Version: "0.0.0",
			})

			s, err := NewPackagesSyncer(
				&internal.NopLogger{},
				packagesAvailable,
				NewMockSender(),
				&ClientSyncedState{},
				store,
				&sync.Mutex{},
				time.Second,
				func(context.Context, *protobufs.DownloadableFile) (*http.Client, error) {
					return &http.Client{}, nil
				},
			)
			require.NoError(t, err)

			// Simulate Sync() so we can test the package installation without worrying about timing
			s.mux.Lock()
			err = s.initStatuses()
			require.NoError(t, err)
			err = s.clientSyncedState.SetPackageStatuses(s.statuses)
			require.NoError(t, err)

			// Run the sync operation
			s.doSync(context.Background())

			// Verify that the package is installed
			packageState, err := store.PackageState("")
			require.NoError(t, err)
			assert.True(t, packageState.Exists)
			assert.Equal(t, packageState.Type, protobufs.PackageType_PackageType_TopLevel)
			assert.Equal(t, packageState.Hash, testFileContentHash[:])
			assert.Equal(t, packageState.Version, "1.0.0")
		})
	}
}

// createTestHTTPServer creates a test HTTP server that serves the given file content
// and returns the server and its URL. The server will be automatically closed
// when the test completes.
func createTestHTTPServer(t *testing.T, fileContent []byte) (*httptest.Server, string) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Write the file content
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(fileContent)
		require.NoError(t, err)
	}))

	// Ensure the server is closed when the test completes
	t.Cleanup(func() {
		server.Close()
	})

	return server, server.URL
}
