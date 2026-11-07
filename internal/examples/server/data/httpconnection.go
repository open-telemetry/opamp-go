package data

import (
	"context"
	"net"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// httpAgentConnection queues UI messages until the Agent's next HTTP poll.
type httpAgentConnection struct {
	conn     types.Connection
	mux      sync.Mutex
	messages []pendingHTTPMessage
}

type pendingHTTPMessage struct {
	message *protobufs.ServerToAgent
	notify  chan<- struct{}
}

func (c *httpAgentConnection) Connection() net.Conn { return c.conn.Connection() }

func (c *httpAgentConnection) Disconnect() error { return c.conn.Disconnect() }

func (c *httpAgentConnection) Send(_ context.Context, msg *protobufs.ServerToAgent) error {
	c.enqueue(msg, nil)
	return nil
}

func (c *httpAgentConnection) enqueue(msg *protobufs.ServerToAgent, notify chan<- struct{}) {
	c.mux.Lock()
	defer c.mux.Unlock()
	c.messages = append(c.messages, pendingHTTPMessage{proto.Clone(msg).(*protobufs.ServerToAgent), notify})
}

func (c *httpAgentConnection) addPendingMessage(response *protobufs.ServerToAgent) chan<- struct{} {
	c.mux.Lock()
	defer c.mux.Unlock()
	if len(c.messages) == 0 {
		return nil
	}
	pending := c.messages[0]
	if response.ConnectionSettings != nil && pending.message.ConnectionSettings != nil {
		// Keep the queued offer intact, including its hash, for a later poll.
		return nil
	}
	proto.Merge(response, pending.message)
	c.messages[0] = pendingHTTPMessage{}
	c.messages = c.messages[1:]
	return pending.notify
}
