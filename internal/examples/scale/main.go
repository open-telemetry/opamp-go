package main

import (
	"context"
	"flag"
	"log"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/open-telemetry/opamp-go/internal/examples/scale/scale"
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

	// Create tls.Config for clients
	// client certs are not currently supported
	tlsCFG, err := (&configtls.ClientConfig{
		Insecure:           tlsInsecure,
		InsecureSkipVerify: tlsInsecureSkipVerify,
		Config: configtls.Config{
			CAFile: tlsCAFile,
		},
	}).LoadTLSConfig(ctx)
	if err != nil {
		logger.Fatalf("Unable to create tls.Config: %v", err)
	}

	logger.Printf("Starting %d agents", agentCount)
	// Create a slice to track agents so we can safely stop them later.
	// Use of slice instead of a concurrent goroutine to reduce memory usage.
	agents := make([]*scale.Agent, 0, agentCount)
	for range agentCount {
		select {
		case <-ctx.Done(): // early termination
			return
		default:
		}

		agent := scale.NewAgent(serverURL, heartbeat, tlsCFG)
		// Use context.Background instead of ctx so we can do a clean shutdown if SIGINT is recieved.
		if err := agent.Start(context.Background()); err != nil {
			logger.Printf("Error starting agent: %v\n", err)
			continue
		}
		agents = append(agents, agent)
	}
	logger.Printf("%d agents started", len(agents))
	<-ctx.Done()
	for _, agent := range agents {
		if err := agent.Stop(); err != nil {
			logger.Printf("Error stopping agent: %v\n", err)
		}
	}
	logger.Println("All agents stopped")

	for _, agent := range agents {
		agent.Wait()
	}
	logger.Println("All agents terminated cleanly")
}
