package helps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// NormalizeKimiConcurrencyError classifies only an explicit Kimi admission
// rejection. Generic access_terminated_error responses may indicate permanent
// access failures and must retain their original status and cooldown policy.
// Call this only before returning an execution error, never to replay a stream.
func NormalizeKimiConcurrencyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) {
		return err
	}
	switch status.StatusCode() {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusTooManyRequests:
	default:
		return err
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(err.Error()), &body) != nil {
		return err
	}
	if body.Error.Type != "access_terminated_error" || strings.TrimSpace(body.Error.Message) != "You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again." {
		return err
	}
	return cliproxyauth.WithCause(&cliproxyauth.Error{
		Code:       cliproxyauth.ErrorCodeUpstreamConcurrency,
		Message:    err.Error(),
		Retryable:  true,
		HTTPStatus: http.StatusServiceUnavailable,
	}, err)
}
