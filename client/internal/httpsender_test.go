package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/testhelpers"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
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
	sender := NewHTTPSender(&sharedinternal.NopLogger{})

	certificate, err := GenerateCertificate()
	assert.NoError(t, err)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
	}

	sender.AddTLSConfig(tlsConfig)
	assert.Equal(t, sender.client.Transport, &http.Transport{TLSClientConfig: tlsConfig})
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

func TestHTTPSenderClosesBodyOnStatusTooManyRequests(t *testing.T) {
	var connectionAttempts int64
	bodyClosed := &atomic.Bool{}

	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt64(&connectionAttempts, 1)
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("retry"))
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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

	originalTransport := sender.client.Transport
	sender.client.Transport = &bodyTrackingTransport{
		transport: originalTransport,
		closed:    bodyClosed,
	}

	resp, err := sender.sendRequestWithRetries(ctx)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Greater(t, atomic.LoadInt64(&connectionAttempts), int64(1), "should have retried after 429")
	assert.True(t, bodyClosed.Load(), "response body should have been closed during retry")
}

// bodyTrackingTransport wraps http.Transport to track response body closures
type bodyTrackingTransport struct {
	transport http.RoundTripper
	closed    *atomic.Bool
	wrapAll   bool // If true, wrap all response bodies, not just retryable ones
}

func (t *bodyTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.transport == nil {
		t.transport = http.DefaultTransport
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.Body != nil && (t.wrapAll || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
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

func TestHTTPSenderClosesBodyOnReceiveResponseError(t *testing.T) {
	bodyClosed := &atomic.Bool{}

	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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

	originalTransport := sender.client.Transport
	sender.client.Transport = &failingBodyTransport{
		transport: originalTransport,
		closed:    bodyClosed,
	}

	resp, err := sender.sendRequestWithRetries(ctx)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	sender.receiveResponse(ctx, resp)
	assert.True(t, bodyClosed.Load(), "response body should have been closed even when reading fails")
}

// setupTestSender creates a test HTTPSender with a standard message.
func setupTestSender(t *testing.T, url string) *HTTPSender {
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

// AI Generated: TestAttemptRequest verifies attemptRequest behavior for all scenarios using table-driven tests.
func TestAttemptRequest(t *testing.T) {
	tests := []struct {
		name                string
		setupServer         func(*testing.T) *MockServer
		setupSender         func(*testing.T, *HTTPSender)
		setupContext        func() context.Context
		currentInterval     time.Duration
		wantRetry           bool
		wantErr             bool
		wantErrContains     string
		wantStatusCode      int
		wantIntervalGreater bool
		wantOnConnectCalled bool
	}{
		{
			name: "Success_StatusOK",
			setupServer: func(t *testing.T) *MockServer {
				srv := StartMockServer(t)
				t.Cleanup(srv.Close)
				srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}
				return srv
			},
			setupSender: func(t *testing.T, sender *HTTPSender) {
				// OnConnect will be checked after attemptRequest call
			},
			setupContext:        context.Background,
			currentInterval:     0,
			wantRetry:           false,
			wantErr:             false,
			wantStatusCode:      http.StatusOK,
			wantOnConnectCalled: true,
		},
		{
			name: "Retryable_StatusTooManyRequests",
			setupServer: func(t *testing.T) *MockServer {
				srv := StartMockServer(t)
				t.Cleanup(srv.Close)
				srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(http.StatusTooManyRequests)
				}
				return srv
			},
			setupSender:         func(*testing.T, *HTTPSender) {},
			setupContext:        context.Background,
			currentInterval:     time.Second,
			wantRetry:           true,
			wantErr:             true,
			wantErrContains:     "server response code=429",
			wantIntervalGreater: true,
		},
		{
			name: "Retryable_StatusServiceUnavailable",
			setupServer: func(t *testing.T) *MockServer {
				srv := StartMockServer(t)
				t.Cleanup(srv.Close)
				srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				return srv
			},
			setupSender:         func(*testing.T, *HTTPSender) {},
			setupContext:        context.Background,
			currentInterval:     time.Second,
			wantRetry:           true,
			wantErr:             true,
			wantErrContains:     "server response code=503",
			wantIntervalGreater: true,
		},
		{
			name: "NonRetryable_StatusBadRequest",
			setupServer: func(t *testing.T) *MockServer {
				srv := StartMockServer(t)
				t.Cleanup(srv.Close)
				srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
				}
				return srv
			},
			setupSender:     func(*testing.T, *HTTPSender) {},
			setupContext:    context.Background,
			currentInterval: 0,
			wantRetry:       false,
			wantErr:         true,
			wantErrContains: "invalid response from server: 400",
		},
		{
			name: "NonRetryable_StatusNotFound",
			setupServer: func(t *testing.T) *MockServer {
				srv := StartMockServer(t)
				t.Cleanup(srv.Close)
				srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}
				return srv
			},
			setupSender:     func(*testing.T, *HTTPSender) {},
			setupContext:    context.Background,
			currentInterval: 0,
			wantRetry:       false,
			wantErr:         true,
			wantErrContains: "invalid response from server: 404",
		},
		{
			name:        "NetworkError_InvalidHost",
			setupServer: nil, // No server for network error
			setupSender: func(t *testing.T, sender *HTTPSender) {
				// Use an invalid URL to force a network error
				sender.url = "http://invalid-host-that-does-not-exist:9999"
			},
			setupContext:    context.Background,
			currentInterval: 0,
			wantRetry:       true,
			wantErr:         true,
		},
		{
			name:        "ContextCanceled",
			setupServer: nil,
			setupSender: func(t *testing.T, sender *HTTPSender) {
				sender.url = "http://example.com"
			},
			setupContext: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // Cancel immediately
				return ctx
			},
			currentInterval: 0,
			wantRetry:       false,
			wantErr:         true,
			wantErrContains: "", // Will check for context.Canceled
		},
		{
			name: "BodyClosed_RetryableStatus",
			setupServer: func(t *testing.T) *MockServer {
				srv := StartMockServer(t)
				t.Cleanup(srv.Close)
				srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusTooManyRequests)
					w.Write([]byte("retry"))
				}
				return srv
			},
			setupSender: func(t *testing.T, sender *HTTPSender) {
				bodyClosed := &atomic.Bool{}
				originalTransport := sender.client.Transport
				sender.client.Transport = &bodyTrackingTransport{
					transport: originalTransport,
					closed:    bodyClosed,
				}
				t.Cleanup(func() {
					assert.True(t, bodyClosed.Load(), "response body should have been closed")
				})
			},
			setupContext:    context.Background,
			currentInterval: 0,
			wantRetry:       true,
			wantErr:         true,
		},
		{
			name: "BodyClosed_NonRetryableStatus",
			setupServer: func(t *testing.T) *MockServer {
				srv := StartMockServer(t)
				t.Cleanup(srv.Close)
				srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte("error"))
				}
				return srv
			},
			setupSender: func(t *testing.T, sender *HTTPSender) {
				bodyClosed := &atomic.Bool{}
				originalTransport := sender.client.Transport
				sender.client.Transport = &bodyTrackingTransport{
					transport: originalTransport,
					closed:    bodyClosed,
					wrapAll:   true, // Wrap all responses to track body closing
				}
				t.Cleanup(func() {
					assert.True(t, bodyClosed.Load(), "response body should have been closed")
				})
			},
			setupContext:    context.Background,
			currentInterval: 0,
			wantRetry:       false,
			wantErr:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var srv *MockServer
			if tt.setupServer != nil {
				srv = tt.setupServer(t)
			}

			ctx := tt.setupContext()
			var url string
			if srv != nil {
				url = "http://" + srv.Endpoint
			} else {
				url = "http://example.com" // Will be overridden by setupSender if needed
			}

			sender := setupTestSender(t, url)
			if tt.setupSender != nil {
				tt.setupSender(t, sender)
			}

			req, err := sender.prepareRequest(ctx)
			assert.NoError(t, err)
			assert.NotNil(t, req)

			// Handle context cancellation for context test
			if errors.Is(ctx.Err(), context.Canceled) {
				req.Request = req.Request.WithContext(ctx)
			}

			// Setup OnConnect callback for success test
			var connected *atomic.Bool
			if tt.wantOnConnectCalled {
				connected = &atomic.Bool{}
				sender.callbacks = types.Callbacks{
					OnConnect: func(ctx context.Context) {
						connected.Store(true)
					},
				}
			}

			result := sender.attemptRequest(ctx, req, tt.currentInterval)

			assert.Equal(t, tt.wantRetry, result.retry, "retry flag mismatch")
			if tt.wantErr {
				assert.Error(t, result.err)
				if tt.wantErrContains != "" {
					assert.Contains(t, result.err.Error(), tt.wantErrContains)
				}
				if tt.name == "ContextCanceled" {
					assert.True(t, errors.Is(result.err, context.Canceled))
				}
			} else {
				assert.NoError(t, result.err)
			}

			if tt.wantStatusCode != 0 {
				assert.NotNil(t, result.resp)
				assert.Equal(t, tt.wantStatusCode, result.resp.StatusCode)
			} else {
				assert.Nil(t, result.resp)
			}

			if tt.wantIntervalGreater {
				assert.Greater(t, result.interval, time.Duration(0), "should set retry interval")
			}

			if tt.wantOnConnectCalled && connected != nil {
				assert.True(t, connected.Load(), "OnConnect should be called")
			}
		})
	}
}

// TestSendRequestWithRetriesContextCancellation verifies that timer is stopped when context is cancelled.
func TestSendRequestWithRetriesContextCancellation(t *testing.T) {
	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	// Server will always return 429 to force retries
	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sender := setupTestSender(t, "http://"+srv.Endpoint)
	sender.callbacks = types.Callbacks{
		OnConnect: func(ctx context.Context) {
		},
		OnConnectFailed: func(ctx context.Context, _ error) {
		},
	}

	// Cancel context after a short delay to ensure timer is created
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	resp, err := sender.sendRequestWithRetries(ctx)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Nil(t, resp)
}

// TestSendRequestWithRetriesOnConnectFailed verifies that OnConnectFailed is called on retryable errors.
func TestSendRequestWithRetriesOnConnectFailed(t *testing.T) {
	var connectionAttempts int64
	connectFailedCalled := &atomic.Bool{}

	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt64(&connectionAttempts, 1)
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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
		OnConnectFailed: func(ctx context.Context, err error) {
			connectFailedCalled.Store(true)
		},
	}
	sender.url = "http://" + srv.Endpoint

	resp, err := sender.sendRequestWithRetries(ctx)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Greater(t, atomic.LoadInt64(&connectionAttempts), int64(1), "should have retried")
	assert.True(t, connectFailedCalled.Load(), "OnConnectFailed should be called on retryable error")
}

// TestSendRequestWithRetriesRetryInterval verifies that retry interval is properly updated.
func TestSendRequestWithRetriesRetryInterval(t *testing.T) {
	var connectionAttempts int64

	srv := StartMockServer(t)
	t.Cleanup(srv.Close)

	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt64(&connectionAttempts, 1)
		if attempt == 1 {
			// Server suggests retrying after 1 second
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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
	sender.url = "http://" + srv.Endpoint

	start := time.Now()
	resp, err := sender.sendRequestWithRetries(ctx)
	duration := time.Since(start)

	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Greater(t, atomic.LoadInt64(&connectionAttempts), int64(1), "should have retried")
	// Should wait at least 1 second due to Retry-After header
	assert.True(t, duration >= time.Second, "should respect Retry-After header")
}

// failingBodyTransport wraps http.Transport to inject a failing body reader
type failingBodyTransport struct {
	transport http.RoundTripper
	closed    *atomic.Bool
}

func (t *failingBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.transport == nil {
		t.transport = http.DefaultTransport
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.Body != nil {
		originalBody := resp.Body
		resp.Body = &failingReadCloser{
			ReadCloser: originalBody,
			closed:     t.closed,
			failOnRead: true,
		}
	}

	return resp, nil
}

type failingReadCloser struct {
	io.ReadCloser
	closed     *atomic.Bool
	failOnRead bool
}

func (b *failingReadCloser) Read(p []byte) (n int, err error) {
	if b.failOnRead {
		return 0, io.ErrUnexpectedEOF
	}
	return b.ReadCloser.Read(p)
}

func (b *failingReadCloser) Close() error {
	if b.closed != nil {
		b.closed.Store(true)
	}
	return b.ReadCloser.Close()
}
