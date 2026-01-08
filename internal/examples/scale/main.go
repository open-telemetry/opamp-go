package main

import (
	"context"
	"flag"
	"log"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/internal/examples/agent/agent"
	"github.com/open-telemetry/opamp-go/internal/examples/config"
	"go.opentelemetry.io/collector/config/configtls"
)

func main() {
	logger := log.New(os.Stdout, "scale-test: ", log.Ldate|log.Lmicroseconds|log.Lmsgprefix)

	var agentCount uint64
	flag.Uint64Var(&agentCount, "agent-count", 1000, "The number of agents to start.")

	var serverURL string
	flag.StringVar(&serverURL, "server-url", "wss://127.0.0.1:4320/v1/opamp", "OpAMP server URL")

	var heartbeat time.Duration
	flag.DurationVar(&heartbeat, "heartbeat", time.Second*30, "Heartbeat duration")

	var tlsInsecure bool
	flag.BoolVar(&tlsInsecure, "tls-insecure", false, "Disable the client transport security.")

	var tlsInsecureSkipVerify bool
	flag.BoolVar(&tlsInsecureSkipVerify, "tls-insecure_skip_verify", false, "Will enable TLS but not verify the certificate.")

	var tlsCAFile string
	flag.StringVar(&tlsCAFile, "tls-ca_file", "", "Path to the CA cert. It verifies the server certificate")

	flag.Parse()

	// Verify args
	if agentCount == 0 {
		logger.Fatal("Arg: agent-count must not be zero")
	}

	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		logger.Fatalf("Arg: server-url failed to parse: %v", err)
	}
	switch parsedURL.Scheme {
	case "http", "https":
	case "ws", "wss":
	default:
		logger.Fatalf("Arg: server-url has an unknown scheme: %v", parsedURL.Scheme)
	}

	if heartbeat < 0 {
		logger.Fatalf("Arg: heartbeat must be non-negative, got %s", heartbeat)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cfg := &config.AgentConfig{
		Endpoint:          serverURL,
		HeartbeatInterval: &heartbeat,
		TLSSetting: configtls.ClientConfig{
			Insecure:           tlsInsecure,
			InsecureSkipVerify: tlsInsecureSkipVerify,
			Config: configtls.Config{
				CAFile: tlsCAFile,
			},
		},
	}

	logger.Printf("Starting %d agents", agentCount)
	// Create a slice to track agents so we can safely stop them later.
	// Use of slice instead of a concurrent goroutine to reduce memory usage.
	agents := make([]*agent.Agent, 0, agentCount)
	for range agentCount {
		select {
		case <-ctx.Done(): // early termination
			return
		default:
		}

		id, err := uuid.NewV7()
		if err != nil {
			panic(err)
		}
		agentLogger := agent.NewScaleLogger(id)
		a := agent.NewAgent(cfg, agent.WithNoClientCertRequest(), agent.WithInstanceID(id), agent.WithLogger(agentLogger))

		if err := a.Start(); err != nil {
			// start errors currently only occur if there is a TLS config error, or the URL scheme is incorrect
			// If the server is unavailable, an agent will retry
			logger.Printf("Error starting agent: %v\n", err)
			continue
		}
		agents = append(agents, a)
	}
	logger.Printf("%d agents started", len(agents))

	<-ctx.Done()
	for _, a := range agents {
		a.Shutdown()
	}
	logger.Println("All agents stopped")
}
