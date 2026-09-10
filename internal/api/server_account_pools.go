package api

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"net/http"
	"strings"
)

func (s *Server) accountPoolRequestGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.handlers == nil || s.handlers.AuthManager == nil || !s.handlers.AuthManager.AccountPoolsEnabled() {
			c.Next()
			return
		}
		// Realtime media and SIP have independent credential/session lifecycles.
		// Until they carry group authorization on every control operation, fail closed.
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/v1/live") || strings.HasPrefix(path, "/v1/realtime") {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "pool_routing_unsupported", "message": "Realtime/Live is not yet available with account groups enabled"}})
			return
		}
		if _, err := s.handlers.AuthManager.AccountPoolScope(c.Request.Context()); err != nil {
			c.AbortWithStatusJSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusForbidden), gin.H{"error": gin.H{"code": "pool_access_denied", "message": err.Error()}})
			return
		}
		c.Next()
	}
}
