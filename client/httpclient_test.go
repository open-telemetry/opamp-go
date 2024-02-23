package client

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestHTTPPolling(t *testing.T) {
	// Start a Server.
	srv := internal.StartMockServer(t)
	var rcvCounter int64
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		assert.EqualValues(t, rcvCounter, msg.SequenceNum)
		if msg != nil {
			atomic.AddInt64(&rcvCounter, 1)
		}
		return nil
	}

	// Start a client.
	settings := types.StartSettings{}
	settings.OpAMPServerURL = "http://" + srv.Endpoint
	client := NewHTTP(nil)
	prepareClient(t, &settings, client)

	// Shorten the polling interval to speed up the test.
	client.sender.SetPollingInterval(time.Millisecond * 10)

	assert.NoError(t, client.Start(context.Background(), settings))

	// Verify that status report is delivered.
	eventually(t, func() bool { return atomic.LoadInt64(&rcvCounter) == 1 })

	// Verify that status report is delivered again. Polling should ensure this.
	eventually(t, func() bool { return atomic.LoadInt64(&rcvCounter) == 2 })

	// Shutdown the Server.
	srv.Close()

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestHTTPClientCompression(t *testing.T) {
	srv := internal.StartMockServer(t)
	var reqCounter int64

	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&reqCounter, 1)
		assert.Equal(t, "gzip", r.Header.Get("Content-Encoding"))
		reader, err := gzip.NewReader(r.Body)
		assert.NoError(t, err)
		body, err := io.ReadAll(reader)
		assert.NoError(t, err)
		_ = r.Body.Close()
		var response protobufs.AgentToServer
		err = proto.Unmarshal(body, &response)
		assert.NoError(t, err)
		assert.Equal(t, response.AgentDescription.IdentifyingAttributes, []*protobufs.KeyValue{
			{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "otelcol"}},
			},
			{
				Key:   "service.namespace",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "default"}},
			},
			{
				Key:   "service.instance.id",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "443e083c-b968-4428-a281-6867bd280e0d"}},
			},
			{
				Key:   "service.version",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "1.0.0"}},
			},
		})
		w.WriteHeader(http.StatusOK)
	}

	settings := types.StartSettings{EnableCompression: true}
	settings.OpAMPServerURL = "http://" + srv.Endpoint
	client := NewHTTP(nil)
	prepareClient(t, &settings, client)

	client.sender.SetPollingInterval(time.Millisecond * 10)

	assert.NoError(t, client.Start(context.Background(), settings))

	eventually(t, func() bool { return atomic.LoadInt64(&reqCounter) == 1 })

	srv.Close()

	err := client.Stop(context.Background())
	assert.NoError(t, err)
}

func TestHTTPClientSetPollingInterval(t *testing.T) {
	// Start a Server.
	srv := internal.StartMockServer(t)
	var rcvCounter int64
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		assert.EqualValues(t, rcvCounter, msg.SequenceNum)
		if msg != nil {
			atomic.AddInt64(&rcvCounter, 1)
		}
		return nil
	}

	// Start a client.
	settings := types.StartSettings{}
	settings.OpAMPServerURL = "http://" + srv.Endpoint
	client := NewHTTP(nil)
	client.SetPollingInterval(100 * time.Millisecond)
	prepareClient(t, &settings, client)

	assert.NoError(t, client.Start(context.Background(), settings))

	// Verify that status report is delivered.
	eventually(t, func() bool { return atomic.LoadInt64(&rcvCounter) == 1 })

	// Verify that status report is delivered again. no call is made for next 100ms
	assert.Eventually(t, func() bool { return atomic.LoadInt64(&rcvCounter) == 2 }, 5*time.Second, 100*time.Millisecond)

	// Shutdown the Server.
	srv.Close()

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}
