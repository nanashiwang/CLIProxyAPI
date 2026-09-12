package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"net/http"
)

func (h *BaseAPIHandler) FilterModelsForAccountPools(c *gin.Context, models []map[string]any) ([]map[string]any, bool) {
	if h == nil || h.AuthManager == nil {
		return models, true
	}
	filtered, err := h.AuthManager.FilterAccountPoolModels(c.Request.Context(), models)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: clienterror.HTTPStatusFromErrorOr(err, http.StatusForbidden), Error: err})
		return nil, false
	}
	return filtered, true
}
