package middleware

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// UsageRequestMetadataMiddleware snapshots connection facts before dispatch,
// including routes that publish usage without BaseAPIHandler. Match request logs:
// RemoteAddr is the peer address; untrusted forwarding headers are not consulted.
func UsageRequestMetadataMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request != nil {
			peer := strings.TrimSpace(c.Request.RemoteAddr)
			if host, _, err := net.SplitHostPort(peer); err == nil {
				peer = host
			}
			ctx := coreusage.WithClientRequestMetadata(c.Request.Context(), coreusage.ClientRequestMetadata{
				Transport: "http",
				ClientIP:  peer,
				UserAgent: c.Request.UserAgent(),
			})
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}
}
