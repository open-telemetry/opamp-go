package internal

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestHTTPSenderRetryForStatusTooManyRequests(t *testing.T) {
	var connectionAttempts int64
	srv := StartMockServer(t)
	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt64(&connectionAttempts, 1)
		// Return a Retry-After header with a value of 1 second for first attempt.
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	url := "http://" + srv.Endpoint
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.AgentDescription = &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "test-service"},
				},
			}},
		}
	})
	sender.callbacks = types.Callbacks{
		OnConnect: func(ctx context.Context) {
		},
		OnConnectFailed: func(ctx context.Context, _ error) {
		},
	}
	sender.url = url
	start := time.Now()
	resp, err := sender.sendRequestWithRetries(ctx)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, time.Since(start) > time.Second)
	cancel()
	srv.Close()
}

func TestHTTPSenderSetHeartbeatInterval(t *testing.T) {
	sender := NewHTTPSender(&sharedinternal.NopLogger{})

	// Default interval should be 30s as per OpAMP Specification
	assert.Equal(t, (30 * time.Second).Milliseconds(), sender.pollingIntervalMs)

	// zero is invalid for http sender
	assert.Error(t, sender.SetHeartbeatInterval(0))
	assert.Equal(t, (30 * time.Second).Milliseconds(), sender.pollingIntervalMs)

	// negative interval is invalid for http sender
	assert.Error(t, sender.SetHeartbeatInterval(-1))
	assert.Equal(t, (30 * time.Second).Milliseconds(), sender.pollingIntervalMs)

	// zero should be valid for http sender
	expected := 10 * time.Second
	assert.NoError(t, sender.SetHeartbeatInterval(expected))
	assert.Equal(t, expected.Milliseconds(), sender.pollingIntervalMs)
}

func TestAddTLSConfig(t *testing.T) {
	certificate, err := GenerateCertificate()
	assert.NoError(t, err)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
	}

	// Custom RoundTripper for testing non-*http.Transport case
	type customTransport struct {
		http.RoundTripper
	}

	tests := []struct {
		name              string
		existingTransport http.RoundTripper
		tlsConfig         *tls.Config
		validateFunc      func(*testing.T, *HTTPSender)
	}{
		{
			name:              "nil TLS config",
			existingTransport: nil,
			tlsConfig:         nil,
			validateFunc: func(t *testing.T, sender *HTTPSender) {
				assert.Nil(t, sender.client.Transport, "transport should remain nil when config is nil")
			},
		},
		{
			name: "nil TLS config with existing transport",
			existingTransport: &http.Transport{
				MaxResponseHeaderBytes: 4096,
			},
			tlsConfig: nil,
			validateFunc: func(t *testing.T, sender *HTTPSender) {
				transport, ok := sender.client.Transport.(*http.Transport)
				assert.True(t, ok, "transport should remain *http.Transport")
				assert.Equal(t, int64(4096), transport.MaxResponseHeaderBytes)
			},
		},
		{
			name:              "valid TLS config with nil existing transport",
			existingTransport: nil,
			tlsConfig:         tlsConfig,
			validateFunc: func(t *testing.T, sender *HTTPSender) {
				transport, ok := sender.client.Transport.(*http.Transport)
				assert.True(t, ok, "transport should be *http.Transport")
				assert.NotNil(t, transport.TLSClientConfig)
				assert.Equal(t, tlsConfig, transport.TLSClientConfig)
			},
		},
		{
			name: "preserve existing transport settings",
			existingTransport: &http.Transport{
				MaxResponseHeaderBytes: 1024,
				MaxIdleConns:           100,
				IdleConnTimeout:        90 * time.Second,
			},
			tlsConfig: tlsConfig,
			validateFunc: func(t *testing.T, sender *HTTPSender) {
				transport, ok := sender.client.Transport.(*http.Transport)
				assert.True(t, ok, "transport should be *http.Transport")
				assert.NotNil(t, transport.TLSClientConfig)
				assert.Equal(t, tlsConfig, transport.TLSClientConfig)
				// Verify existing settings were preserved
				assert.Equal(t, int64(1024), transport.MaxResponseHeaderBytes)
				assert.Equal(t, 100, transport.MaxIdleConns)
				assert.Equal(t, 90*time.Second, transport.IdleConnTimeout)
			},
		},
		{
			name:              "non-*http.Transport",
			existingTransport: &customTransport{},
			tlsConfig:         tlsConfig,
			validateFunc: func(t *testing.T, sender *HTTPSender) {
				transport, ok := sender.client.Transport.(*http.Transport)
				assert.True(t, ok, "transport should be *http.Transport")
				assert.NotNil(t, transport.TLSClientConfig)
				assert.Equal(t, tlsConfig, transport.TLSClientConfig)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := NewHTTPSender(&sharedinternal.NopLogger{})
			sender.client.Transport = tc.existingTransport

			sender.AddTLSConfig(tc.tlsConfig)

			tc.validateFunc(t, sender)
		})
	}
}

func GenerateCertificate() (tls.Certificate, error) {
	certPem := []byte(`-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----`)

	keyPem := []byte(`-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIIrYSSNQFaA2Hwf1duRSxKtLYX5CB04fSeQ6tF1aY/PuoAoGCCqGSM49
AwEHoUQDQgAEPR3tU2Fta9ktY+6P9G0cWO+0kETA6SFs38GecTyudlHz6xvCdz8q
EKTcWGekdmdDPsHloRNtsiCa697B2O9IFA==
-----END EC PRIVATE KEY-----`)

	cert, err := tls.X509KeyPair(certPem, keyPem)
	if err != nil {
		return tls.Certificate{}, err
	}

	return cert, nil
}

func TestHTTPSenderRetryForFailedRequests(t *testing.T) {
	srv, m := newMockServer(t)
	address := testhelpers.GetAvailableLocalAddress()
	var connectionAttempts int64

	var buf []byte
	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt64(&connectionAttempts, 1)
		if attempt == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("server doesn't support hijacking")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		} else {
			buf, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	url := "http://" + address
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.AgentDescription = &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "test-service"},
				},
			}},
		}
	})
	sender.callbacks = types.Callbacks{
		OnConnect: func(ctx context.Context) {
		},
		OnConnectFailed: func(ctx context.Context, _ error) {
		},
	}
	sender.url = url
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		sender.sendRequestWithRetries(ctx)
		wg.Done()
	}()
	go func() {
		l, err := net.Listen("tcp", address)
		assert.NoError(t, err)
		ts := httptest.NewUnstartedServer(m)
		ts.Listener.Close()
		ts.Listener = l
		ts.Start()
		srv.srv = ts
		wg.Done()
	}()
	wg.Wait()
	assert.True(t, len(buf) > 0)
	assert.Contains(t, string(buf), "test-service")
	cancel()
	srv.Close()
}

func TestRequestInstanceUidFlagReset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.callbacks = types.Callbacks{}
	sender.callbacks.SetDefaults()

	// Set the RequestInstanceUid flag on the tracked state to request the server for a new ID to use.
	clientSyncedState := &ClientSyncedState{}
	clientSyncedState.SetFlags(protobufs.AgentToServerFlags_AgentToServerFlags_RequestInstanceUid)
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_Unspecified
	clientSyncedState.SetCapabilities(&capabilities)
	sender.receiveProcessor = newReceivedProcessor(&sharedinternal.NopLogger{}, sender.callbacks, sender, clientSyncedState, nil, new(sync.Mutex), time.Second)

	// If we process a message with a nil AgentIdentification, or an incorrect NewInstanceUid.
	sender.receiveProcessor.ProcessReceivedMessage(ctx,
		&protobufs.ServerToAgent{
			AgentIdentification: nil,
		})
	sender.receiveProcessor.ProcessReceivedMessage(ctx,
		&protobufs.ServerToAgent{
			AgentIdentification: &protobufs.AgentIdentification{NewInstanceUid: []byte("foo")},
		})

	// Then the RequestInstanceUid flag stays intact.
	assert.Equal(t, sender.receiveProcessor.clientSyncedState.flags, protobufs.AgentToServerFlags_AgentToServerFlags_RequestInstanceUid)

	// If we process a message that contains a non-nil AgentIdentification that contains a NewInstanceUid.
	sender.receiveProcessor.ProcessReceivedMessage(ctx,
		&protobufs.ServerToAgent{
			AgentIdentification: &protobufs.AgentIdentification{NewInstanceUid: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}},
		})

	// Then the flag is reset so we don't request a new instance uid yet again.
	assert.Equal(t, sender.receiveProcessor.clientSyncedState.flags, protobufs.AgentToServerFlags_AgentToServerFlags_Unspecified)
	cancel()
}

func TestPackageUpdatesInParallel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	localPackageState := NewInMemPackagesStore()
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	blockSyncCh := make(chan struct{})
	doneCh := make([]<-chan struct{}, 0)

	// Use `ch` to simulate blocking behavior on the second call to Sync().
	// This will allow both Sync() calls to be called in parallel; we will
	// first make sure that both are inflight before manually releasing the
	// channel so that both go through in sequence.
	localPackageState.onAllPackagesHash = func() {
		if localPackageState.lastReportedStatuses != nil {
			<-blockSyncCh
		}
	}

	var messages atomic.Int32
	var mux sync.Mutex
	callbacks := types.Callbacks{}
	callbacks.SetDefaults()
	callbacks.OnMessage = func(ctx context.Context, msg *types.MessageData) {
		err := msg.PackageSyncer.Sync(ctx)
		assert.NoError(t, err)
		messages.Add(1)
		doneCh = append(doneCh, msg.PackageSyncer.Done())
	}
	sender.callbacks = callbacks

	clientSyncedState := &ClientSyncedState{}
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages
	clientSyncedState.SetCapabilities(&capabilities)
	sender.receiveProcessor = newReceivedProcessor(&sharedinternal.NopLogger{}, sender.callbacks, sender, clientSyncedState, localPackageState, &mux, time.Second)

	sender.receiveProcessor.ProcessReceivedMessage(ctx,
		&protobufs.ServerToAgent{
			PackagesAvailable: &protobufs.PackagesAvailable{
				Packages: map[string]*protobufs.PackageAvailable{
					"package1": {
						Type:    protobufs.PackageType_PackageType_TopLevel,
						Version: "1.0.0",
						File: &protobufs.DownloadableFile{
							DownloadUrl: "foo",
							ContentHash: []byte{4, 5},
						},
						Hash: []byte{1, 2, 3},
					},
				},
				AllPackagesHash: []byte{1, 2, 3, 4, 5},
			},
		})
	sender.receiveProcessor.ProcessReceivedMessage(ctx,
		&protobufs.ServerToAgent{
			PackagesAvailable: &protobufs.PackagesAvailable{
				Packages: map[string]*protobufs.PackageAvailable{
					"package22": {
						Type:    protobufs.PackageType_PackageType_TopLevel,
						Version: "1.0.0",
						File: &protobufs.DownloadableFile{
							DownloadUrl: "bar",
							ContentHash: []byte{4, 5},
						},
						Hash: []byte{1, 2, 3},
					},
				},
				AllPackagesHash: []byte{1, 2, 3, 4, 5},
			},
		})

	// Make sure that both Sync calls have gone through _before_ releasing the first one.
	// This means that they're both called in parallel, and that the race
	// detector would always report a race condition, but proper locking makes
	// sure that's not the case.
	assert.Eventually(t, func() bool {
		return messages.Load() == 2
	}, 2*time.Second, 100*time.Millisecond, "both messages must have been processed successfully")

	// Release the second Sync call so it can continue and wait for both of them to complete.
	blockSyncCh <- struct{}{}
	<-doneCh[0]
	<-doneCh[1]

	cancel()
}

func TestPackageUpdatesWithError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sender := NewHTTPSender(&sharedinternal.NopLogger{})

	// We'll pass in a nil PackageStateProvider to force the Sync call to return with an error.
	localPackageState := types.PackagesStateProvider(nil)
	var messages atomic.Int32
	var mux sync.Mutex

	callbacks := types.Callbacks{}
	callbacks.SetDefaults()
	callbacks.OnMessage = func(ctx context.Context, msg *types.MessageData) {
		// Make sure the call to Sync will return an error due to a nil PackageStateProvider
		err := msg.PackageSyncer.Sync(ctx)
		assert.Error(t, err)
		messages.Add(1)
	}
	sender.callbacks = callbacks

	clientSyncedState := &ClientSyncedState{}
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages
	clientSyncedState.SetCapabilities(&capabilities)

	sender.receiveProcessor = newReceivedProcessor(&sharedinternal.NopLogger{}, sender.callbacks, sender, clientSyncedState, localPackageState, &mux, time.Second)

	// Send two messages in parallel.
	sender.receiveProcessor.ProcessReceivedMessage(ctx,
		&protobufs.ServerToAgent{
			PackagesAvailable: &protobufs.PackagesAvailable{},
		})
	sender.receiveProcessor.ProcessReceivedMessage(ctx,
		&protobufs.ServerToAgent{
			PackagesAvailable: &protobufs.PackagesAvailable{},
		})

	// Make sure that even though the call to Sync errored out early, the lock
	// was still released properly for both messages to be processed.
	assert.Eventually(t, func() bool {
		return messages.Load() == 2
	}, 5*time.Second, 100*time.Millisecond, "both messages must have been processed successfully")

	cancel()
}

func TestHTTPSenderSetProxy(t *testing.T) {
	tests := []struct {
		name string
		url  string
		err  error
	}{{
		name: "http proxy",
		url:  "http://proxy.internal:8080",
		err:  nil,
	}, {
		name: "socks5 proxy",
		url:  "socks5://proxy.internal:8080",
		err:  nil,
	}, {
		name: "no schema",
		url:  "proxy.internal:8080",
		err:  nil,
	}, {
		name: "empty url",
		url:  "",
		err:  url.InvalidHostError(""),
	}, {
		name: "invalid url",
		url:  "this is not valid",
		err:  url.InvalidHostError("this is not valid"),
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := NewHTTPSender(&sharedinternal.NopLogger{})
			err := sender.SetProxy(tc.url, nil)
			if tc.err != nil {
				assert.ErrorAs(t, err, &tc.err)
			} else {
				assert.NoError(t, err)
			}
		})
	}

	t.Run("old transport settings are preserved", func(t *testing.T) {
		sender := &HTTPSender{
			client: &http.Client{
				Transport: &http.Transport{
					MaxResponseHeaderBytes: 1024,
				},
			},
		}
		err := sender.SetProxy("https://proxy.internal:8080", nil)
		assert.NoError(t, err)
		transport, ok := sender.client.Transport.(*http.Transport)
		if !ok {
			t.Logf("Transport: %v", sender.client.Transport)
			t.Fatalf("Unable to coorce as *http.Transport detected type: %T", sender.client.Transport)
		}
		assert.NotNil(t, transport.Proxy)
		assert.Equal(t, int64(1024), transport.MaxResponseHeaderBytes)
	})

	t.Run("test https proxy", func(t *testing.T) {
		var connected atomic.Bool
		// HTTPS Connect proxy, no auth required
		proxyServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			t.Logf("Request: %+v", req)
			if req.Method != http.MethodConnect {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			connected.Store(true)

			targetConn, err := net.DialTimeout("tcp", req.Host, 10*time.Second)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer targetConn.Close()

			hijacker, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			clientConn, _, err := hijacker.Hijack()
			if err != nil {
				t.Logf("Hijack error: %v", err)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
			defer clientConn.Close()

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, err := io.Copy(targetConn, clientConn)
				assert.NoError(t, err, "proxy encountered an error copying to destination")
			}()
			go func() {
				defer wg.Done()
				_, err := io.Copy(clientConn, targetConn)
				assert.NoError(t, err, "proxy encountered an error copying to client")
			}()
			wg.Wait()
		}))
		t.Cleanup(proxyServer.Close)

		srv := StartTLSMockServer(t)
		t.Cleanup(srv.Close)
		srv.OnRequest = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}

		sender := NewHTTPSender(&sharedinternal.NopLogger{})
		sender.client = proxyServer.Client()
		err := sender.SetProxy(proxyServer.URL, http.Header{"test-header": []string{"test-value"}})
		assert.NoError(t, err)

		t.Logf("Proxy URL: %s", proxyServer.URL)

		sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.AgentDescription = &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{{
					Key: "service.name",
					Value: &protobufs.AnyValue{
						Value: &protobufs.AnyValue_StringValue{StringValue: "test-service"},
					},
				}},
			}
		})
		sender.callbacks = types.Callbacks{
			OnConnect: func(_ context.Context) {
			},
			OnConnectFailed: func(_ context.Context, err error) {
				t.Logf("sender failed to connect: %v", err)
			},
		}
		sender.url = "https://" + srv.Endpoint

		resp, err := sender.sendRequestWithRetries(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, connected.Load(), "test request did not use proxy")
	})
}

func TestHTTPSenderClosesBody(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		shouldRetry    bool
		eventualStatus int
	}{
		{
			name:           "Retryable_StatusTooManyRequests",
			statusCode:     http.StatusTooManyRequests,
			shouldRetry:    true,
			eventualStatus: http.StatusOK,
		},
		{
			name:           "Retryable_StatusServiceUnavailable",
			statusCode:     http.StatusServiceUnavailable,
			shouldRetry:    true,
			eventualStatus: http.StatusOK,
		},
		{
			name:           "NonRetryable_StatusBadRequest",
			statusCode:     http.StatusBadRequest,
			shouldRetry:    false,
			eventualStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var connectionAttempts int64
			bodyClosed := &atomic.Bool{}

			srv := StartMockServer(t)
			t.Cleanup(srv.Close)

			srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
				attempt := atomic.AddInt64(&connectionAttempts, 1)
				if tt.shouldRetry && attempt == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(tt.statusCode)
					w.Write([]byte("retry"))
				} else {
					w.WriteHeader(tt.eventualStatus)
					w.Write([]byte("test"))
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			sender := setupTestSender(t, "http://"+srv.Endpoint)
			sender.callbacks = types.Callbacks{
				OnConnect: func(ctx context.Context) {
				},
				OnConnectFailed: func(ctx context.Context, _ error) {
				},
			}

			originalTransport := sender.client.Transport
			sender.client.Transport = &bodyTrackingTransport{
				transport: originalTransport,
				closed:    bodyClosed,
			}

			_, err := sender.sendRequestWithRetries(ctx)

			assert.Equal(t, tt.shouldRetry, err == nil)
			assert.True(t, bodyClosed.Load(), "response body should have been closed")
		})
	}
}

// bodyTrackingTransport wraps http.Transport to track response body closures
type bodyTrackingTransport struct {
	transport http.RoundTripper
	closed    *atomic.Bool
}

func (t *bodyTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.transport == nil {
		t.transport = http.DefaultTransport
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.Body != nil {
		originalBody := resp.Body
		resp.Body = &closeTrackingBodyWrapper{
			ReadCloser: originalBody,
			closed:     t.closed,
		}
	}

	return resp, nil
}

type closeTrackingBodyWrapper struct {
	io.ReadCloser
	closed *atomic.Bool
}

func (b *closeTrackingBodyWrapper) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

// setupTestSender creates a test HTTPSender with a standard message.
func setupTestSender(_ *testing.T, url string) *HTTPSender {
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.AgentDescription = &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "test-service"},
				},
			}},
		}
	})
	sender.url = url
	return sender
}
