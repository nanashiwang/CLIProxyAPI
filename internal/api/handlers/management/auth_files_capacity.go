package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GetAuthFileCapacity returns a bounded process-local snapshot for visible cards.
func (h *Handler) GetAuthFileCapacity(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var input struct {
		IDs []string `json:"ids"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
	if err := c.ShouldBindJSON(&input); err != nil || len(input.IDs) == 0 || len(input.IDs) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "capacity_ids_required", "message": "ids must contain 1 to 200 account IDs"})
		return
	}
	for _, id := range input.IDs {
		if strings.TrimSpace(id) == "" || len(id) > 512 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_capacity_id"})
			return
		}
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "capacity_observer_unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": h.authManager.ExecutionCapacities(input.IDs)})
}
