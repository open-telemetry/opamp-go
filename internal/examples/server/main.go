package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/internal/examples/server/opampsrv"
	"github.com/open-telemetry/opamp-go/internal/examples/server/uisrv"
	"github.com/open-telemetry/opamp-go/signing"
)

var logger = log.New(log.Default().Writer(), "[MAIN] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

func main() {
	var emitMetrics bool
	flag.BoolVar(&emitMetrics, "emit-metrics", false, "Emit metrics to stdout.")

	var policyServerURL string
	flag.StringVar(&policyServerURL, "policy-server", "",
		"Base URL of the out-of-process policy/signing server (e.g. http://localhost:4322).\n"+
			"When set, every outbound ServerToAgent message is signed via that server,\n"+
			"demonstrating the isolated Message Attestation signing architecture.\n"+
			"Run internal/examples/policysrv first to start a local policy server.")

	flag.Parse()

	curDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	// If a policy server URL is provided, create a RemoteSigner that delegates
	// all signing to it. The OpAMP server itself never touches the private key.
	var payloadSigner signing.Signer
	if policyServerURL != "" {
		rs := signing.NewRemoteSigner(policyServerURL)
		// Short chain-cache TTL so the demo picks up policy-server leaf
		// rotation promptly (production can keep the longer default).
		rs.SetChainCacheTTL(2 * time.Second)
		payloadSigner = rs
		logger.Printf("Message Attestation enabled — signing via policy server at %s", policyServerURL)
	}

	logger.Println("OpAMP Server starting...")

	uisrv.Start(curDir)
	opampSrv := opampsrv.NewServer(&data.AllAgents, emitMetrics, payloadSigner)
	opampSrv.Start()

	logger.Println("OpAMP Server running...")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	<-interrupt

	logger.Println("OpAMP Server shutting down...")
	uisrv.Shutdown()
	opampSrv.Stop()
}
