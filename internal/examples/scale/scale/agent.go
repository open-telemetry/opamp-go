package scale

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

const (
	serviceName       = "io.opentelemetry.opamp.scale"
	serviceVersion    = "0.1.0"
	scaleCapabilities = protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus
)

var agentDescription = protobufs.AgentDescription{
	IdentifyingAttributes: []*protobufs.KeyValue{{
		Key: "service.name",
		Value: &protobufs.AnyValue{
			Value: &protobufs.AnyValue_StringValue{StringValue: serviceName},
		},
	}, {
		Key: "service.version",
		Value: &protobufs.AnyValue{
			Value: &protobufs.AnyValue_StringValue{StringValue: serviceVersion},
		},
	}},
}

type Agent struct {
	logger   types.Logger
	client   client.OpAMPClient
	settings types.StartSettings

	runningCh chan struct{} // used to determine if the agent has is running
}

func NewAgent(serverURL string, heartbeat time.Duration, tlsCFG *tls.Config) *Agent {
	uid, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	logger := NewLogger(uid)

	var opampClient client.OpAMPClient
	if strings.HasPrefix(serverURL, "http") { // is http(s) connection
		opampClient = client.NewHTTP(logger)
	} else { // is a websockets connection
		opampClient = client.NewWebSocket(logger)
	}

	return &Agent{
		logger: logger,
		client: opampClient,
		settings: types.StartSettings{
			OpAMPServerURL:    serverURL,
			TLSConfig:         tlsCFG,
			InstanceUid:       types.InstanceUid(uid),
			HeartbeatInterval: &heartbeat,
		},
	}
}

// Start starts the OpAMP client with the passed context.
func (a *Agent) Start(ctx context.Context) error {
	err := a.client.SetAgentDescription(&agentDescription)
	if err != nil {
		return fmt.Errorf("unable to set agent description: %w", err)
	}
	capabilities := scaleCapabilities
	err = a.client.SetCapabilities(&capabilities)
	if err != nil {
		return fmt.Errorf("unable to set agent capabilities: %w", err)
	}

	err = a.client.Start(ctx, a.settings)
	if err != nil {
		return fmt.Errorf("unable to start OpAMP client: %w", err)
	}
	a.runningCh = make(chan struct{})
	return nil
}

// Stop stops the OpAMP client with a 5s timeout.
func (a *Agent) Stop() error {
	if a.runningCh == nil {
		return fmt.Errorf("agent has not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	defer close(a.runningCh)
	return a.client.Stop(ctx)
}

// Wait will wait until a running agent stops.
func (a *Agent) Wait() error {
	if a.runningCh == nil {
		return fmt.Errorf("agent has not started")
	}
	<-a.runningCh
	return nil
}
