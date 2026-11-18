package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/poo"
)

func (h *BaseAPIHandler) pooConfig() config.PoOParentGatewayConfig {
	if cfg := h.CurrentConfig(); cfg != nil {
		return cfg.PoOParentGateway
	}
	return config.PoOParentGatewayConfig{}
}

func (h *BaseAPIHandler) pooEnabled() bool { return h.pooConfig().Enabled }

func (h *BaseAPIHandler) pooRequired() bool {
	cfg := h.pooConfig()
	return cfg.Enabled && cfg.IsRequired()
}

func (h *BaseAPIHandler) finishPoONonStream(body []byte, headers http.Header) ([]byte, *interfaces.ErrorMessage) {
	cfg := h.pooConfig()
	if !cfg.Enabled {
		poo.TakeRecordID(headers)
		return body, nil
	}
	recordID := poo.TakeRecordID(headers)
	if recordID == "" {
		if cfg.IsRequired() {
			return nil, pooOutputError(fmt.Errorf("PoO transport was bypassed"))
		}
		return body, nil
	}
	proof, err := poo.AwaitResult(recordID, cfg.RequestTimeout())
	if err != nil {
		if cfg.IsRequired() {
			return nil, pooOutputError(err)
		}
		return body, nil
	}
	result, err := poo.InjectProof(body, proof)
	if err != nil {
		return nil, pooOutputError(fmt.Errorf("inject PoO proof: %w", err))
	}
	return result, nil
}

func pooOutputError(err error) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      fmt.Errorf("TEE proof verification path failed: %w", err),
	}
}

func pooStreamErrorPayload(err error) []byte {
	message := "TEE proof unavailable"
	if err != nil {
		message = err.Error()
	}
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "tee_error",
			"message": message,
		},
	})
	return payload
}
