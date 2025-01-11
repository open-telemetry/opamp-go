package main

import (
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/internal/examples/server/opampsrv"
	"github.com/open-telemetry/opamp-go/internal/examples/server/uisrv"
)

var logger = log.New(log.Default().Writer(), "[MAIN] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

func main() {
	curDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	logger.Println("OpAMP Server starting...")

	var sendCA bool
	flag.BoolVar(&sendCA, "send-ca", false, "Send the CA the OpAMP server uses in the initial response as part of offered settings.")

	flag.Parse()

	uisrv.Start(curDir)
	opampSrv := opampsrv.NewServer(&data.AllAgents, sendCA)
	opampSrv.Start()

	logger.Println("OpAMP Server running...")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	<-interrupt

	logger.Println("OpAMP Server shutting down...")
	uisrv.Shutdown()
	opampSrv.Stop()
}
