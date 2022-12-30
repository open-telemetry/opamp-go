package server

import (
	"net/http"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

type CallbacksStruct struct {
	OnConnectingFunc func(request *http.Request) types.ConnectionResponse
}

var _ types.Callbacks = (*CallbacksStruct)(nil)

func (c CallbacksStruct) OnConnecting(request *http.Request) types.ConnectionResponse {
	if c.OnConnectingFunc != nil {
		return c.OnConnectingFunc(request)
	}
	return types.ConnectionResponse{Accept: true}
}

type ConnectionCallbacksStruct struct {
	OnConnectedFunc       func(conn types.Connection)
	OnMessageFunc         func(conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent
	OnConnectionCloseFunc func(conn types.Connection)
}

var _ types.ConnectionCallbacks = (*ConnectionCallbacksStruct)(nil)

func (c ConnectionCallbacksStruct) OnConnected(conn types.Connection) {
	if c.OnConnectedFunc != nil {
		c.OnConnectedFunc(conn)
	}
}

func (c ConnectionCallbacksStruct) OnMessage(conn types.Connection, message *protobufs.AgentToServer) *protobufs.ServerToAgent {
	if c.OnMessageFunc != nil {
		return c.OnMessageFunc(conn, message)
	} else {
		// We will send an empty response since there is no user-defined callback to handle it.
		return &protobufs.ServerToAgent{
			InstanceUid: message.InstanceUid,
		}
	}
}

func (c ConnectionCallbacksStruct) OnConnectionClose(conn types.Connection) {
	if c.OnConnectionCloseFunc != nil {
		c.OnConnectionCloseFunc(conn)
	}
}
