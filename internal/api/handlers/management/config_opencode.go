package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// GetOpenCode returns the native OpenCode Zen/Go provider configuration.
// Credential values are intentionally omitted; the panel can retain an existing
// credential by sending its source index and the configured marker back on PUT.
func (h *Handler) GetOpenCode(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusOK, gin.H{"opencode": config.OpenCodeConfig{}})
		return
	}

	h.mu.Lock()
	value := h.cfg.OpenCode
	value.Zen = maskOpenCodeTier(value.Zen)
	value.Go = maskOpenCodeTier(value.Go)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"opencode": value})
}

// PutOpenCode replaces the native OpenCode configuration and reloads it without
// requiring a process restart. The endpoint is deliberately whole-object based:
// Zen/Go share global settings, while each tier owns a separate key list.
func (h *Handler) PutOpenCode(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}

	var envelope struct {
		OpenCode *config.OpenCodeConfig `json:"opencode"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.OpenCode == nil {
		var value config.OpenCodeConfig
		if unmarshalErr := json.Unmarshal(data, &value); unmarshalErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		envelope.OpenCode = &value
	}

	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config is unavailable"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	previous := h.cfg.OpenCode
	candidate := &config.Config{OpenCode: *envelope.OpenCode}
	if errMerge := mergeOpenCodeCredentialSecrets(&candidate.OpenCode, &previous); errMerge != nil {
		c.JSON(http.StatusConflict, gin.H{"error": errMerge.Error()})
		return
	}
	candidate.SanitizeOpenCode()
	if err := validateOpenCodeManagementConfig(candidate); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.cfg.OpenCode = candidate.OpenCode
	if !h.persistLocked(c) {
		// Do not leave an in-memory config that was not durably written.
		h.cfg.OpenCode = previous
	}
}

func maskOpenCodeTier(tier config.OpenCodeTierConfig) config.OpenCodeTierConfig {
	entries := make([]config.OpenCodeAPIKey, len(tier.APIKeyEntries))
	for index, entry := range tier.APIKeyEntries {
		entries[index] = maskOpenCodeKey(entry, index)
	}
	tier.APIKeyEntries = entries
	tier.Headers = maskOpenCodeHeaders(tier.Headers)
	for index := range tier.APIKeyEntries {
		tier.APIKeyEntries[index].Headers = maskOpenCodeHeaders(tier.APIKeyEntries[index].Headers)
	}
	return tier
}

func maskOpenCodeKey(entry config.OpenCodeAPIKey, index int) config.OpenCodeAPIKey {
	apiKey := strings.TrimSpace(entry.APIKey)
	entry.APIKey = ""
	entry.Note = strings.TrimSpace(entry.Note)
	entry.APIKeyConfigured = apiKey != ""
	entry.APIKeyPreview = util.HideAPIKey(apiKey)
	entry.SourceIndex = &index
	return entry
}

func maskOpenCodeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	masked := make(map[string]string, len(headers))
	for key, value := range headers {
		masked[key] = util.MaskSensitiveHeaderValue(key, value)
	}
	return masked
}

// mergeOpenCodeCredentialSecrets restores credentials hidden by GetOpenCode.
// SourceIndex keeps deletion/reordering in the panel from binding a masked key
// to the wrong entry. A stale masked value is rejected instead of silently
// overwriting a credential changed by another management client.
func mergeOpenCodeCredentialSecrets(candidate, previous *config.OpenCodeConfig) error {
	if candidate == nil || previous == nil {
		return nil
	}
	var err error
	candidate.Zen, err = mergeOpenCodeTierSecrets(candidate.Zen, previous.Zen, "zen")
	if err != nil {
		return err
	}
	candidate.Go, err = mergeOpenCodeTierSecrets(candidate.Go, previous.Go, "go")
	return err
}

func mergeOpenCodeTierSecrets(
	candidate, previous config.OpenCodeTierConfig,
	tierName string,
) (config.OpenCodeTierConfig, error) {
	var err error
	candidate.APIKeyEntries, err = mergeOpenCodeTierKeys(
		candidate.APIKeyEntries,
		previous.APIKeyEntries,
		tierName,
	)
	if err != nil {
		return candidate, err
	}
	candidate.Headers = mergeOpenCodeHeaders(candidate.Headers, previous.Headers)
	for index := range candidate.APIKeyEntries {
		previousIndex := index
		if candidate.APIKeyEntries[index].SourceIndex != nil {
			previousIndex = *candidate.APIKeyEntries[index].SourceIndex
		}
		if previousIndex >= 0 && previousIndex < len(previous.APIKeyEntries) {
			candidate.APIKeyEntries[index].Headers = mergeOpenCodeHeaders(
				candidate.APIKeyEntries[index].Headers,
				previous.APIKeyEntries[previousIndex].Headers,
			)
		}
	}
	return candidate, nil
}

func mergeOpenCodeTierKeys(
	candidate []config.OpenCodeAPIKey,
	previous []config.OpenCodeAPIKey,
	tierName string,
) ([]config.OpenCodeAPIKey, error) {
	merged := make([]config.OpenCodeAPIKey, len(candidate))
	copy(merged, candidate)
	for index := range merged {
		entry := &merged[index]
		if strings.TrimSpace(entry.APIKey) != "" || !entry.APIKeyConfigured {
			continue
		}
		if entry.SourceIndex == nil {
			return nil, fmt.Errorf("opencode.%s.api-key-entries[%d] is missing source-index; reload and try again", tierName, index)
		}
		sourceIndex := *entry.SourceIndex
		if sourceIndex < 0 || sourceIndex >= len(previous) {
			return nil, fmt.Errorf("opencode.%s.api-key-entries[%d] refers to a stale credential; reload and try again", tierName, index)
		}
		previousKey := strings.TrimSpace(previous[sourceIndex].APIKey)
		if entry.APIKeyPreview != "" && entry.APIKeyPreview != util.HideAPIKey(previousKey) {
			return nil, fmt.Errorf("opencode.%s.api-key-entries[%d] refers to a changed credential; reload and try again", tierName, index)
		}
		entry.APIKey = previous[sourceIndex].APIKey
	}
	return merged, nil
}

func mergeOpenCodeHeaders(candidate, previous map[string]string) map[string]string {
	if len(candidate) == 0 || len(previous) == 0 {
		return candidate
	}
	merged := make(map[string]string, len(candidate))
	for key, value := range candidate {
		for previousKey, previousValue := range previous {
			if !strings.EqualFold(key, previousKey) || value != util.MaskSensitiveHeaderValue(previousKey, previousValue) {
				continue
			}
			merged[key] = previousValue
			break
		}
		if _, exists := merged[key]; !exists {
			merged[key] = value
		}
	}
	return merged
}

func validateOpenCodeManagementConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("opencode config is required")
	}
	if !config.IsAllowedOpenCodeBaseURLForTier(cfg.OpenCode.Zen.BaseURL, "zen") {
		return fmt.Errorf("opencode.zen.base-url must be exactly %s", config.DefaultOpenCodeZenURL)
	}
	if !config.IsAllowedOpenCodeBaseURLForTier(cfg.OpenCode.Go.BaseURL, "go") {
		return fmt.Errorf("opencode.go.base-url must be exactly %s", config.DefaultOpenCodeGoURL)
	}
	if err := cfg.ValidateOpenCode(); err != nil {
		return err
	}

	// Keep this endpoint aligned with the runtime's accepted values even when
	// OpenCode is currently disabled, so a later toggle cannot revive an invalid
	// upstream URL from the management panel.
	if strings.TrimSpace(cfg.OpenCode.Prefer) != "zen" && strings.TrimSpace(cfg.OpenCode.Prefer) != "go" {
		return fmt.Errorf("opencode.prefer must be zen or go")
	}
	return nil
}
