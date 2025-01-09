package main

import (
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/open-telemetry/opamp-go/internal/examples/agent/agent"
)

func main() {
	var agentType string
	flag.StringVar(&agentType, "t", "io.opentelemetry.collector", "Agent Type String")

	var agentVersion string
	flag.StringVar(&agentVersion, "v", "1.0.0", "Agent Version String")

	var requestConnectionSettings bool
	flag.BoolVar(&requestConnectionSettings, "request-connection-settings", false, "Request offered connection settings with the initial message.")

	flag.Parse()

	agent := agent.NewAgent(&agent.Logger{log.Default()}, agentType, agentVersion, requestConnectionSettings)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	<-interrupt
	agent.Shutdown()
}
