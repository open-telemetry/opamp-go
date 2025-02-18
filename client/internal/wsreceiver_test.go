package internal

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var _ types.Logger = &TestLogger{}

type TestLogger struct {
	*testing.T
}

func (logger TestLogger) Debugf(ctx context.Context, format string, v ...interface{}) {
	logger.Logf(format, v...)
}

func (logger TestLogger) Errorf(ctx context.Context, format string, v ...interface{}) {
	logger.Fatalf(format, v...)
}

type commandAction int

const (
	none commandAction = iota
	restart
	unknown
)

func TestServerToAgentCommand(t *testing.T) {

	tests := []struct {
		command *protobufs.ServerToAgentCommand
		action  commandAction
		message string
	}{
		{
			command: nil,
			action:  none,
			message: "No command should result in no action",
		},
		{
			command: &protobufs.ServerToAgentCommand{
				Type: protobufs.CommandType_CommandType_Restart,
			},
			action:  restart,
			message: "A Restart command should result in a restart",
		},
		{
			command: &protobufs.ServerToAgentCommand{
				Type: -1,
			},
			action:  unknown,
			message: "An unknown command is still passed to the OnCommand callback",
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			action := none

			callbacks := types.Callbacks{
				OnCommand: func(ctx context.Context, command *protobufs.ServerToAgentCommand) error {
					switch command.Type {
					case protobufs.CommandType_CommandType_Restart:
						action = restart
					default:
						action = unknown
					}
					return nil
				},
			}
			callbacks.SetDefaults()
			clientSyncedState := ClientSyncedState{
				remoteConfigStatus: &protobufs.RemoteConfigStatus{},
			}
			sender := WSSender{}
			capabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand
			metrics := types.NewClientMetrics(64)
			receiver := NewWSReceiver(TestLogger{t}, callbacks, nil, &sender, &clientSyncedState, nil, capabilities, new(sync.Mutex), metrics)
			receiver.processor.ProcessReceivedMessage(context.Background(), &protobufs.ServerToAgent{
				Command: test.command,
			})
			assert.Equal(t, test.action, action, test.message)
		})
	}
}

func TestServerToAgentCommandExclusive(t *testing.T) {
	cases := []struct {
		capabilities             protobufs.AgentCapabilities
		command                  *protobufs.ServerToAgentCommand
		calledCommand            bool
		calledCommandMsg         string
		calledOnMessageConfig    bool
		calledOnMessageConfigMsg string
	}{
		{
			capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand,
			command: &protobufs.ServerToAgentCommand{
				Type: protobufs.CommandType_CommandType_Restart,
			},
			calledCommand:            true,
			calledCommandMsg:         "OnCommand should be called when a Command is specified and capabilities are set",
			calledOnMessageConfig:    false,
			calledOnMessageConfigMsg: "OnMessage should not be called when a Command is specified and capabilities are set",
		},
		{
			capabilities: protobufs.AgentCapabilities_AgentCapabilities_Unspecified,
			command: &protobufs.ServerToAgentCommand{
				Type: protobufs.CommandType_CommandType_Restart,
			},
			calledCommand:            false,
			calledCommandMsg:         "OnCommand should not be called when a Command is specified and capabilities are not set",
			calledOnMessageConfig:    true,
			calledOnMessageConfigMsg: "OnMessage should be called when a Command is specified and capabilities are not set",
		},
	}

	for _, test := range cases {
		calledCommand := false
		calledOnMessageConfig := false

		callbacks := types.Callbacks{
			OnCommand: func(ctx context.Context, command *protobufs.ServerToAgentCommand) error {
				calledCommand = true
				return nil
			},
			OnMessage: func(ctx context.Context, msg *types.MessageData) {
				calledOnMessageConfig = true
			},
		}
		clientSyncedState := ClientSyncedState{}
		mux := new(sync.Mutex)
		metrics := types.NewClientMetrics(64)
		receiver := NewWSReceiver(TestLogger{t}, callbacks, nil, nil, &clientSyncedState, nil, test.capabilities, mux, metrics)
		receiver.processor.ProcessReceivedMessage(context.Background(), &protobufs.ServerToAgent{
			Command: &protobufs.ServerToAgentCommand{
				Type: protobufs.CommandType_CommandType_Restart,
			},
			RemoteConfig: &protobufs.AgentRemoteConfig{},
		})
		assert.Equal(t, test.calledCommand, calledCommand, test.calledCommandMsg)
		assert.Equal(t, test.calledOnMessageConfig, calledOnMessageConfig, test.calledOnMessageConfigMsg)

	}
}

func TestDecodeMessage(t *testing.T) {
	msgsToTest := []*protobufs.ServerToAgent{
		{}, // Empty message
		{
			InstanceUid: []byte("0123456789123456"),
		},
	}

	// Try with and without header byte. This is only necessary until the
	// end of grace period that ends Feb 1, 2023. After that the header is
	// no longer optional.
	withHeaderTests := []bool{false, true}

	for _, msg := range msgsToTest {
		for _, withHeader := range withHeaderTests {
			bytes, err := proto.Marshal(msg)
			require.NoError(t, err)

			if withHeader {
				// Prepend zero header byte.
				bytes = append([]byte{0}, bytes...)
			}

			var decoded protobufs.ServerToAgent
			err = internal.DecodeWSMessage(bytes, &decoded)
			require.NoError(t, err)

			assert.True(t, proto.Equal(msg, &decoded))
		}
	}
}

func TestReceiverLoopStop(t *testing.T) {

	srv := StartMockServer(t)

	conn, _, err := websocket.DefaultDialer.DialContext(
		context.Background(),
		"ws://"+srv.Endpoint,
		nil,
	)
	require.NoError(t, err)

	var receiverLoopStopped atomic.Bool

	callbacks := types.Callbacks{}
	clientSyncedState := ClientSyncedState{
		remoteConfigStatus: &protobufs.RemoteConfigStatus{},
	}
	sender := WSSender{}
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand
	metrics := types.NewClientMetrics(64)
	receiver := NewWSReceiver(TestLogger{t}, callbacks, conn, &sender, &clientSyncedState, nil, capabilities, new(sync.Mutex), metrics)
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

func TestWSPackageUpdatesInParallel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var messages atomic.Int32
	var mux sync.Mutex
	blockSyncCh := make(chan struct{})
	doneCh := make([]<-chan struct{}, 0)
	localPackageState := NewInMemPackagesStore()

	// Use `ch` to simulate blocking behavior on the second call to Sync().
	// This will allow both Sync() calls to be called in parallel; we will
	// first make sure that both are inflight before manually releasing the
	// channel so that both go through in sequence.
	localPackageState.onAllPackagesHash = func() {
		if localPackageState.lastReportedStatuses != nil {
			<-blockSyncCh
		}
	}
	callbacks := types.Callbacks{
		OnMessage: func(ctx context.Context, msg *types.MessageData) {
			err := msg.PackageSyncer.Sync(ctx)
			assert.NoError(t, err)
			messages.Add(1)
			doneCh = append(doneCh, msg.PackageSyncer.Done())
		},
	}
	clientSyncedState := &ClientSyncedState{}
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages
	sender := NewSender(&internal.NopLogger{})
	metrics := types.NewClientMetrics(64)
	receiver := NewWSReceiver(&internal.NopLogger{}, callbacks, nil, sender, clientSyncedState, localPackageState, capabilities, &mux, metrics)

	receiver.processor.ProcessReceivedMessage(ctx,
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
	receiver.processor.ProcessReceivedMessage(ctx,
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

type wsMsg struct {
	Message []byte
	Error   error
}

type fakeWSConn struct {
	messages chan wsMsg
}

func (f fakeWSConn) ReadMessage() (int, []byte, error) {
	msg := <-f.messages
	return websocket.BinaryMessage, msg.Message, msg.Error
}

type fakewsWriteCloser struct {
	messages chan wsMsg
}

func (f fakewsWriteCloser) Write(msg []byte) (int, error) {
	f.messages <- wsMsg{Message: msg}
	return len(msg), nil
}

func (f fakewsWriteCloser) Close() error {
	return nil
}

func (f fakeWSConn) NextWriter(msgtype int) (io.WriteCloser, error) {
	return fakewsWriteCloser{messages: f.messages}, nil
}

func (f fakeWSConn) WriteControl(int, []byte, time.Time) error {
	return nil
}

func (f fakeWSConn) Close() error {
	return nil
}

func (f fakeWSConn) UnderlyingConn() net.Conn {
	// shouldn't generally be used by us, may need to become a net.Pipe
	return nil
}

func newFakeWSConn() fakeWSConn {
	return fakeWSConn{
		messages: make(chan wsMsg, 128),
	}
}

func TestReceiveMessageMetrics(t *testing.T) {
	msg := &protobufs.ServerToAgent{
		InstanceUid:  []byte("abcd"),
		Capabilities: 2,
	}

	serialMsg, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	callbacks := types.Callbacks{}
	callbacks.SetDefaults()
	clientSyncedState := ClientSyncedState{
		remoteConfigStatus: &protobufs.RemoteConfigStatus{},
	}
	sender := WSSender{}
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand
	metrics := types.NewClientMetrics(64)
	conn := newFakeWSConn()
	conn.messages <- wsMsg{Message: serialMsg}
	receiver := NewWSReceiver(TestLogger{t}, callbacks, conn, &sender, &clientSyncedState, nil, capabilities, new(sync.Mutex), metrics)
	if err := receiver.receiveMessage(new(protobufs.ServerToAgent)); err != nil {
		t.Fatal(err)
	}
	if got, want := metrics.RxMessages.Read(), int64(1); got != want {
		t.Errorf("bad RxMessages count: got %d, want %d", got, want)
	}
	if metrics.RxBytes.Read() == 0 {
		t.Error("RxBytes not counted")
	}
	if metrics.RxErrors.Read() != 0 {
		t.Error("error counted when there is none")
	}
	metadata := metrics.RxMessageInfo.Drain()
	if got, want := len(metadata), 1; got != want {
		t.Errorf("unexpected metadata buffer length: got %d, want %d", got, want)
	}
	got := metadata[0]
	want := types.RxMessageInfo{
		InstanceUID:  []byte("abcd"),
		Capabilities: 2,
		Attrs:        types.MessageAttrs(types.RxMessageAttr | types.ServerToAgentMessageAttr | types.WSTransportAttr),
	}
	if !cmp.Equal(got, want) {
		t.Errorf("metadata not equal: %s", cmp.Diff(want, got))
	}
}
