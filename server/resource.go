package server

import (
	//"errors"
	"github.com/gin-gonic/gin"
	"net/http"
)

type configuration struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var config = configuration{ID: "1", Name: "config"}

func main() {
	router := gin.Default()
	router.GET("/config", fetchConfig)
	router.POST("/config", createConfig)
	router.Run("localhost:2000")
}

func fetchConfig(c *gin.Context) {
	c.IndentedJSON(http.StatusOK, config)
}

func createConfig(c *gin.Context) {
	var newConfig configuration
	config = newConfig
	c.IndentedJSON(http.StatusCreated, config)
}
