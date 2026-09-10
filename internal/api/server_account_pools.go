package api

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) accountPoolRequestGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		instance, user := c.GetHeader("X-CPA-Instance-ID"), c.GetHeader("X-CPA-User-ID")
		if len(c.Request.Header.Values("X-CPA-Instance-ID")) == 1 && len(c.Request.Header.Values("X-CPA-User-ID")) == 1 {
			if id, err := strconv.ParseUint(user, 10, 64); err == nil && id > 0 && strconv.FormatUint(id, 10) == user {
				c.Request = c.Request.WithContext(sdkaccess.WithGatewayIdentity(c.Request.Context(), instance, user))
			}
		}
		c.Request.Header.Del("X-CPA-Instance-ID")
		c.Request.Header.Del("X-CPA-User-ID")

		if s.handlers == nil || s.handlers.AuthManager == nil {
			c.Next()
			return
		}
		// Realtime media and SIP have independent credential/session lifecycles.
		// Until they carry group authorization on every control operation, fail closed.
		path := c.Request.URL.Path
		if s.handlers.AuthManager.AccountPoolsEnabled() && (strings.HasPrefix(path, "/v1/live") || strings.HasPrefix(path, "/v1/realtime")) {
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
