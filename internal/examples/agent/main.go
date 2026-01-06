package main

import (
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/open-telemetry/opamp-go/internal/examples/agent/agent"
	"github.com/open-telemetry/opamp-go/internal/examples/config"
	"go.opentelemetry.io/collector/config/configtls"
)

func main() {
	var agentType string
	flag.StringVar(&agentType, "t", "io.opentelemetry.collector", "Agent Type String")

	var agentVersion string
	flag.StringVar(&agentVersion, "v", "1.0.0", "Agent Version String")

	var tlsInsecure bool
	flag.BoolVar(&tlsInsecure, "tls-insecure", false, "Disable the client transport security.")

	var tlsInsecureSkipVerify bool
	flag.BoolVar(&tlsInsecureSkipVerify, "tls-insecure_skip_verify", false, "Will enable TLS but not verify the certificate.")

	var tlsCertFile string
	flag.StringVar(&tlsCertFile, "tls-cert_file", "", "Path to the TLS cert")

	var tlsKeyFile string
	flag.StringVar(&tlsKeyFile, "tls-key_file", "", "Path to the TLS key")

	var tlsCAFile string
	flag.StringVar(&tlsCAFile, "tls-ca_file", "", "Path to the CA cert. It verifies the server certificate")

	var endpoint string
	flag.StringVar(&endpoint, "endpoint", "wss://127.0.0.1:4320/v1/opamp", "OpAMP server endpoint URL")

	flag.Parse()

	config := &config.AgentConfig{
		Endpoint: endpoint,
		TLSSetting: configtls.ClientConfig{
			Insecure:           tlsInsecure,
			InsecureSkipVerify: tlsInsecureSkipVerify,
			Config: configtls.Config{
				KeyFile:  tlsKeyFile,
				CertFile: tlsCertFile,
				CAFile:   tlsCAFile,
			},
		},
	}

	agent := agent.NewAgent(config, agent.WithAgentType(agentType), agent.WithAgentVersion(agentVersion))
	if err := agent.Start(); err != nil {
		log.Fatal("Agent encountered error when starting: %v", err)
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	<-interrupt
	agent.Shutdown()
}
