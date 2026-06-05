//go:build cwebsocket

package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestReceiverLoopStop(t *testing.T) {
	srv := StartMockServer(t)

	conn, _, err := websocket.Dial(
		context.Background(),
		"ws://"+srv.Endpoint,
		nil,
	)
	require.NoError(t, err)
	conn.SetReadLimit(-1)
	t.Cleanup(func() { _ = conn.CloseNow() })

	var receiverLoopStopped atomic.Bool

	callbacks := types.Callbacks{}
	clientSyncedState := ClientSyncedState{
		remoteConfigStatus: &protobufs.RemoteConfigStatus{},
	}
	sender := WSSender{}
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand
	clientSyncedState.SetCapabilities(&capabilities)
	receiver := NewWSReceiver(TestLogger{t}, callbacks, conn, &sender, &clientSyncedState, nil, new(sync.Mutex), time.Second)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		receiver.ReceiverLoop(ctx)
		receiverLoopStopped.Store(true)
	}()
	cancel()

	assert.Eventually(t, func() bool {
		return receiverLoopStopped.Load()
	}, 2*time.Second, 100*time.Millisecond, "ReceiverLoop should stop when context is cancelled")
}

func TestRecieveMessage(t *testing.T) {
	tests := []struct {
		name     string
		server   func(t *testing.T) *httptest.Server
		hasError bool
	}{{
		name: "binary message",
		server: func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				require.NoError(t, err)
				defer conn.CloseNow()

				uid, err := uuid.NewV7()
				require.NoError(t, err)
				p, err := uid.MarshalBinary()
				require.NoError(t, err)
				response := &protobufs.ServerToAgent{
					InstanceUid: p,
				}
				msg, err := proto.Marshal(response)
				require.NoError(t, err)
				err = conn.Write(r.Context(), websocket.MessageBinary, msg)
				require.NoError(t, err)
				conn.Close(websocket.StatusNormalClosure, "")
			}))
		},
		hasError: false,
	}, {
		name: "text message",
		server: func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				require.NoError(t, err)
				defer conn.CloseNow()

				err = conn.Write(r.Context(), websocket.MessageText, []byte(`Hello, World!`))
				require.NoError(t, err)
				conn.Close(websocket.StatusNormalClosure, "")
			}))
		},
		hasError: true,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.server(t)
			defer srv.Close()

			u, err := url.Parse(srv.URL)
			require.NoError(t, err)

			conn, _, err := websocket.Dial(
				t.Context(),
				"ws://"+u.Host,
				nil,
			)
			require.NoError(t, err)
			conn.SetReadLimit(-1)

			callbacks := types.Callbacks{}
			callbacks.SetDefaults()
			state := &ClientSyncedState{}
			capabilities := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus
			state.SetCapabilities(&capabilities)
			rec := NewWSReceiver(&internal.NopLogger{}, callbacks, conn, NewSender(&internal.NopLogger{}), state, nil, new(sync.Mutex), time.Second)

			err = rec.receiveMessage(t.Context(), &protobufs.ServerToAgent{})
			if tc.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
