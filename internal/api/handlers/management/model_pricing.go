package management

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pricing"
)

// GetModelPricing lists effective base prices from the active catalog and custom overrides.
func (h *Handler) GetModelPricing(c *gin.Context) {
	limit := 200
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsed, errParse := strconv.Atoi(rawLimit)
		if errParse != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_limit", "message": "limit must be a positive integer"})
			return
		}
		limit = parsed
	}
	models := pricing.Default().ListModels(c.Query("q"), limit)
	c.JSON(http.StatusOK, gin.H{
		"count":  len(models),
		"models": models,
	})
}

// GetModelPricingStatus returns catalog source, version, refresh, and cache state.
func (h *Handler) GetModelPricingStatus(c *gin.Context) {
	c.JSON(http.StatusOK, pricing.Default().Status())
}

// PostModelPricingRefresh refreshes the active catalog immediately.
func (h *Handler) PostModelPricingRefresh(c *gin.Context) {
	if !pricing.Default().Status().Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "pricing_disabled", "message": "model pricing is disabled"})
		return
	}
	if errRefresh := pricing.Default().Refresh(c.Request.Context()); errRefresh != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "pricing_refresh_failed", "message": errRefresh.Error(), "status": pricing.Default().Status()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": pricing.Default().Status()})
}

// GetCustomModelPricing returns one exact-model custom price override.
func (h *Handler) GetCustomModelPricing(c *gin.Context) {
	model, okModel := pricingModelFromRequest(c)
	if !okModel {
		return
	}
	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config_unavailable"})
		return
	}
	override, exists := h.cfg.Pricing.Overrides[model]
	h.mu.Unlock()
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "pricing_override_not_found", "model": model})
		return
	}
	c.JSON(http.StatusOK, gin.H{"model": model, "override": clonePricingOverride(override)})
}

// PutCustomModelPricing creates or replaces one exact-model custom price override.
func (h *Handler) PutCustomModelPricing(c *gin.Context) {
	model, okModel := pricingModelFromRequest(c)
	if !okModel {
		return
	}
	var override config.PricingOverride
	if errBindJSON := c.ShouldBindJSON(&override); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errBindJSON.Error()})
		return
	}
	override.Provider = strings.TrimSpace(override.Provider)
	if errValidate := validatePricingOverride(override); errValidate != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_pricing_override", "message": errValidate})
		return
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config_unavailable"})
		return
	}
	if h.cfg.Pricing.Overrides == nil {
		h.cfg.Pricing.Overrides = make(map[string]config.PricingOverride)
	}
	previous, existed := h.cfg.Pricing.Overrides[model]
	h.cfg.Pricing.Overrides[model] = clonePricingOverride(override)
	snapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
	if !okSnapshot {
		if existed {
			h.cfg.Pricing.Overrides[model] = previous
		} else {
			delete(h.cfg.Pricing.Overrides, model)
		}
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "model": model, "override": override})
}

// DeleteCustomModelPricing removes one exact-model custom price override.
func (h *Handler) DeleteCustomModelPricing(c *gin.Context) {
	model, okModel := pricingModelFromRequest(c)
	if !okModel {
		return
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config_unavailable"})
		return
	}
	previous, exists := h.cfg.Pricing.Overrides[model]
	if !exists {
		h.mu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "pricing_override_not_found", "model": model})
		return
	}
	delete(h.cfg.Pricing.Overrides, model)
	snapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
	if !okSnapshot {
		h.cfg.Pricing.Overrides[model] = previous
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "model": model})
}

func pricingModelFromRequest(c *gin.Context) (string, bool) {
	rawModel := strings.TrimPrefix(strings.TrimSpace(c.Param("model")), "/")
	if rawModel == "" {
		rawModel = strings.TrimSpace(c.Query("model"))
	}
	if rawModel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_model", "message": "model is required"})
		return "", false
	}
	return rawModel, true
}

func validatePricingOverride(override config.PricingOverride) string {
	prices := []struct {
		name  string
		value *float64
	}{
		{name: "input", value: override.Input},
		{name: "output", value: override.Output},
		{name: "cache-read", value: override.CacheRead},
		{name: "cache-write", value: override.CacheWrite},
	}
	hasPrice := false
	for _, price := range prices {
		if price.value == nil {
			continue
		}
		hasPrice = true
		if *price.value < 0 || math.IsNaN(*price.value) || math.IsInf(*price.value, 0) {
			return price.name + " must be a finite non-negative USD price per million tokens"
		}
	}
	if !hasPrice {
		return "at least one price bucket is required"
	}
	return ""
}

func clonePricingOverride(override config.PricingOverride) config.PricingOverride {
	cloneFloat := func(value *float64) *float64 {
		if value == nil {
			return nil
		}
		cloned := *value
		return &cloned
	}
	return config.PricingOverride{
		Provider:   strings.TrimSpace(override.Provider),
		Input:      cloneFloat(override.Input),
		Output:     cloneFloat(override.Output),
		CacheRead:  cloneFloat(override.CacheRead),
		CacheWrite: cloneFloat(override.CacheWrite),
	}
}
