package types

import (
	"sync/atomic"
	"time"
)

// NewClientMetrics creates new ClientMetrics with a given buffer size for
// collecting TxMessageInfo and RxMessageInfo. The buffer should be at least
// size 1.
func NewClientMetrics(bufSize int) *ClientMetrics {
	return &ClientMetrics{
		TxMessageInfo: NewRingBuffer[TxMessageInfo](bufSize),
		RxMessageInfo: NewRingBuffer[RxMessageInfo](bufSize),
	}
}

// ClientMetrics contains metrics about an OpAMP client and its interactions
// with an OpAMP server.
type ClientMetrics struct {
	// RxBytes is the total number of bytes received by the client.
	RxBytes Counter

	// RxMessages is the total number of proto messages received by the client.
	RxMessages Counter

	// RxErrors is the total number of errors encountered while receiving
	// messages. Typically, this would be due to interrupted connections or
	// the client receiving a message that could not be decoded.
	RxErrors Counter

	// RxMessageInfo contains details about the last several messages to arrive.
	RxMessageInfo *RingBuffer[RxMessageInfo]

	// TxBytes is the total number of bytes sent during the session.
	TxBytes Counter

	// TxMessages is the total number of proto messages sent during the session.
	TxMessages Counter

	// TxErrors is the total number of errors encountered while sending
	// messages to the server. Typically, this would be due to network errors.
	TxErrors Counter

	// TxMessageInfo contains details about the last several messages that
	// were sent.
	TxMessageInfo *RingBuffer[TxMessageInfo]
}

// Counter is an atomic counter with labels. The labels should not be modified
// after creation.
type Counter struct {
	count  int64
	Labels []string
}

// Add adds the amount to the counter. Negative amounts are OK.
// Add is goroutine-safe.
func (c *Counter) Add(amount int64) {
	atomic.AddInt64(&c.count, amount)
}

// Read reads the current value of the counter. Read is goroutine-safe.
func (c *Counter) Read() int64 {
	return atomic.LoadInt64(&c.count)
}

// TxMessageInfo contains message metadata as well as the transmission latency.
type TxMessageInfo struct {
	// InstanceUID is the UUID of the agent or server. It should be 16 bytes long.
	InstanceUID []byte

	// SequenceNum is the sequence number of the message.
	SequenceNum uint64

	// Capabilities is ServerCapabilities or AgentCapabilities depending on the origin.
	Capabilities uint64

	// Attrs contain message metadata as bitfields.
	Attrs MessageAttrs

	// TxLatency is the time it took for the message to be sent.
	TxLatency time.Duration
}

// RxMessageInfo contains message metadata.
type RxMessageInfo struct {
	// InstanceUID is the UUID of the agent or server. It should be 16 bytes long.
	InstanceUID []byte

	// SequenceNum is the sequence number of the message.
	SequenceNum uint64

	// Capabilities is ServerCapabilities or AgentCapabilities depending on the origin.
	Capabilities uint64

	// Attrs contain message metadata as bitfields.
	Attrs MessageAttrs

	// RxLatency is the time it took for the message to be received. RxLatency
	// is not tracked for the websocket transport, since the protocol is
	// asynchronous.
	RxLatency time.Duration
}

// MessageAttrs are bitsets of MessageAttrs.
type MessageAttrs uint64

// MessageAttr is an individual message attribute.
type MessageAttr uint64

const (
	// TxMessageAttr is set when the message was transmitted to us.
	TxMessageAttr MessageAttr = 1

	// RxMessageAttr is set when the message was sent by us.
	RxMessageAttr MessageAttr = 1 << 1

	// AgentToServerMessageAttr is sent when the message is an AgentToServer message.
	AgentToServerMessageAttr MessageAttr = 1 << 2

	// ServerToAgentMessageAttr is sent when the message is a ServerToAgent message.
	ServerToAgentMessageAttr MessageAttr = 1 << 3

	// ErrorMessageAttr is set when the message is an error response.
	ErrorMessageAttr MessageAttr = 1 << 4

	// HTTPTransportAttr is set when the message was sent on an HTTP transport.
	HTTPTransportAttr MessageAttr = 1 << 5

	// WSTransportAttr is set when the message was sent on a WS transport.
	WSTransportAttr MessageAttr = 1 << 6
)

// Set adds attr to the bitset.
func (m *MessageAttrs) Set(attr MessageAttr) {
	*m = *m | MessageAttrs(attr)
}

// Unset removes attr from the bitset.
func (m *MessageAttrs) Unset(attr MessageAttr) {
	*m = *m & MessageAttrs(^attr)
}

// Slice returns all of the individual attributes as a slice.
func (m *MessageAttrs) Slice() []MessageAttr {
	result := []MessageAttr{}
	selector := MessageAttr(1)
	for i := 0; i < 64; i++ {
		if m.Contains(selector) {
			result = append(result, selector)
		}
		selector = selector << 1
	}
	return result
}

// Contains checks to see if the attr is contained in the bitset.
func (m *MessageAttrs) Contains(attr MessageAttr) bool {
	return (*m & MessageAttrs(attr)) > 0
}
