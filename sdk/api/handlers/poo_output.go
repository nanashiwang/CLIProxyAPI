package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/poo"
)

func (h *BaseAPIHandler) pooEnabled() bool {
	return h != nil && h.Cfg != nil && h.Cfg.PoOParentGateway.Enabled
}

func (h *BaseAPIHandler) pooRequired() bool {
	return h.pooEnabled() && h.Cfg.PoOParentGateway.IsRequired()
}

func (h *BaseAPIHandler) finishPoONonStream(body []byte, headers http.Header) ([]byte, *interfaces.ErrorMessage) {
	if !h.pooEnabled() {
		poo.TakeRecordID(headers)
		return body, nil
	}
	recordID := poo.TakeRecordID(headers)
	if recordID == "" {
		if h.pooRequired() {
			return nil, pooOutputError(fmt.Errorf("PoO transport was bypassed"))
		}
		return body, nil
	}
	proof, err := poo.AwaitResult(recordID, h.Cfg.PoOParentGateway.RequestTimeout())
	if err != nil {
		if h.pooRequired() {
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
