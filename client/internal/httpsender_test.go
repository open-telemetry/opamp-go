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

func TestDelaySchedule(t *testing.T) {
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	pendingMessageChan := sender.hasPendingMessage
	scheduleSendDelayChan := sender.registerScheduleSend
	sender.DisableScheduleSend()

	// Verify ScheduleSend is not writing to message channel when disabled
	sender.ScheduleSend()
	assert.Equal(t, 0, len(pendingMessageChan))
	assert.Equal(t, 1, len(scheduleSendDelayChan))

	// Verify ScheduleSend is writing to message channel when enabled
	sender.EnableScheduleSend()
	assert.Equal(t, 1, len(pendingMessageChan))
	assert.Equal(t, 0, len(scheduleSendDelayChan))

	// ScheduleSend sanity check after enabling
	sender.ScheduleSend()
	assert.Equal(t, 1, len(pendingMessageChan))
	assert.Equal(t, 0, len(scheduleSendDelayChan))

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
