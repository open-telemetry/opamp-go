package internal

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
)

func TestDelayScheduleSender(t *testing.T) {
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	c := sender.hasPendingMessage

	// Verify ScheduleSend() doesn't write messages to channel when disables
	sender.DisableScheduleSend()
	sender.ScheduleSend()
	assert.Equal(t, 0, len(c))

	// Verify ScheduleSend() works properly otherwise
	sender.EnableScheduleSend()
	sender.ScheduleSend()
	assert.Equal(t, 1, len(c))
}

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
		OnConnectFunc: func() {
		},
		OnConnectFailedFunc: func(_ error) {
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
