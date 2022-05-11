package client

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
)

func TestHTTPPolling(t *testing.T) {
	// Start a server.
	srv := internal.StartMockServer(t)
	var rcvCounter int64
	srv.OnMessage = func(msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		if statusReport := msg.GetStatusReport(); statusReport != nil {
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

	// Shutdown the server.
	srv.Close()

	// Shutdown the client.
	err := client.Stop(context.Background())
	assert.NoError(t, err)
}
