package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ping is a cheap liveness endpoint for keep-alive pingers, so the free
// Render instance is not spun down after 15 minutes of inactivity.
func ping(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.String(http.StatusOK, "pong")
}
