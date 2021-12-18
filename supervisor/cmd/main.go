package main

import (
	"flag"
	"github.com/open-telemetry/opamp-go/supervisor"
	"log"
	"os"
)

func main() {
	var configFile string
	flag.StringVar(&configFile, "c", "", "Supervisor configuration file")
	flag.Parse()

	logger := &supervisor.Logger{Logger: log.Default()}
	if err := supervisor.New(configFile, logger).Start(); err != nil {
		os.Exit(1)
	}
}
