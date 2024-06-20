package internal

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	sender.callbacks = types.CallbacksStruct{
		OnConnectFunc: func(ctx context.Context) {
		},
		OnConnectFailedFunc: func(ctx context.Context, _ error) {
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
	sender.callbacks = types.CallbacksStruct{
		OnConnectFunc: func(ctx context.Context) {
		},
		OnConnectFailedFunc: func(ctx context.Context, _ error) {
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
	sender.callbacks = types.CallbacksStruct{}

	// Set the RequestInstanceUid flag on the tracked state to request the server for a new ID to use.
	clientSyncedState := &ClientSyncedState{}
	clientSyncedState.SetFlags(protobufs.AgentToServerFlags_AgentToServerFlags_RequestInstanceUid)
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_Unspecified
	sender.receiveProcessor = newReceivedProcessor(&sharedinternal.NopLogger{}, sender.callbacks, sender, clientSyncedState, nil, capabilities)

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
