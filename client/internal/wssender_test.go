package internal

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
)

func TestWSSenderSetHeartbeatInterval(t *testing.T) {
	sender := NewSender(&sharedinternal.NopLogger{})

	// Default interval should be 30s as per OpAMP Specification
	assert.Equal(t, int64((30 * time.Second).Milliseconds()), sender.heartbeatIntervalMs.Load())

	// negative interval is invalid for http sender
	assert.Error(t, sender.SetHeartbeatInterval(-1))
	assert.Equal(t, int64((30 * time.Second).Milliseconds()), sender.heartbeatIntervalMs.Load())

	// zero is valid for ws sender
	assert.NoError(t, sender.SetHeartbeatInterval(0))
	assert.Equal(t, int64(0), sender.heartbeatIntervalMs.Load())

	var expected int64 = 10000
	assert.NoError(t, sender.SetHeartbeatInterval(time.Duration(expected)*time.Millisecond))
	assert.Equal(t, expected, sender.heartbeatIntervalMs.Load())
}

func TestWSSendMessageMetrics(t *testing.T) {
	msg := &protobufs.AgentToServer{
		InstanceUid:  []byte("abcd"),
		Capabilities: 2,
		SequenceNum:  1,
	}

	sender := NewSender(TestLogger{T: t})
	metrics := types.NewClientMetrics(64)
	sender.SetMetrics(metrics)
	wsConn := newFakeWSConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sender.Start(ctx, wsConn); err != nil {
		t.Fatal(err)
	}
	// this would be better to do within the interface of the sender, but by
	// calling this method we bypass its internal queue which simplifies things
	// somewhat
	if err := sender.sendMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if got, want := metrics.TxMessages.Read(), int64(1); got != want {
		t.Errorf("bad TxMessages count: got %d, want %d", got, want)
	}
	if metrics.TxBytes.Read() == 0 {
		t.Error("TxBytes not counted")
	}
	if metrics.TxErrors.Read() != 0 {
		t.Errorf("TxErrors counted when there were none")
	}
	messageInfo := metrics.TxMessageInfo.Drain()
	if len(messageInfo) != 1 {
		t.Fatal("wrong messageInfo len")
	}
	mi := messageInfo[0]
	if got, want := mi.InstanceUID, []byte("abcd"); !cmp.Equal(got, want) {
		t.Errorf("bad InstanceUID: got %v, want %v", got, want)
	}
	if got, want := mi.Capabilities, uint64(2); got != want {
		t.Errorf("bad Capabilities: got %d, want %d", got, want)
	}
	if got, want := mi.SequenceNum, uint64(1); got != want {
		t.Errorf("bad SequenceNum: got %d, want %d", got, want)
	}
	if mi.TxLatency == 0 {
		t.Error("latency not tracked")
	}
}
