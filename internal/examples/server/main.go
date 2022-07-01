package main

import (
	//"github.com/gin-gonic/gin"
	//"net/http"

	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/internal/examples/server/opampsrv"
	"github.com/open-telemetry/opamp-go/internal/examples/server/uisrv"
	"log"
	"os"
	"os/signal"
)

var logger = log.New(log.Default().Writer(), "[MAIN] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

//type configuration struct {
//	ID   string `json:"id"`
//	Name string `json:"name"`
//}
//
//var config = configuration{ID: "1", Name: "config"}

func main() {
	//router := gin.Default()
	//router.GET("/config", fetchConfig)
	//router.POST("/config", createConfig)
	//router.Run()

	curDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	logger.Println("OpAMP Server starting...")

	uisrv.Start(curDir)
	opampSrv := opampsrv.NewServer(&data.AllAgents)
	opampSrv.Start()

	logger.Println("OpAMP Server running...")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	<-interrupt

	logger.Println("OpAMP Server shutting down...")
	uisrv.Shutdown()
	opampSrv.Stop()
}

//func fetchConfig(c *gin.Context) {
//	c.IndentedJSON(http.StatusOK, config)
//}
//
//func createConfig(c *gin.Context) {
//	var newConfig configuration
//	config = newConfig
//	c.IndentedJSON(http.StatusCreated, config)
//}
