package helps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type kimiTestStatusError struct {
	code int
	body string
}

func (e kimiTestStatusError) Error() string   { return e.body }
func (e kimiTestStatusError) StatusCode() int { return e.code }

func TestNormalizeKimiConcurrencyError(t *testing.T) {
	const busy = `{"error":{"type":"access_terminated_error","message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."}}`
	for _, code := range []int{400, 403, 429} {
		original := kimiTestStatusError{code, busy}
		got := NormalizeKimiConcurrencyError(original)
		var classified *auth.Error
		if !errors.As(got, &classified) || classified.Code != auth.ErrorCodeUpstreamConcurrency || classified.HTTPStatus != 503 || !classified.Retryable {
			t.Fatalf("code=%d: unexpected result %v", code, got)
		}
		if !errors.Is(got, original) {
			t.Fatal("lost original error")
		}
	}
	cases := []error{
		nil, context.Canceled, fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		kimiTestStatusError{401, busy}, kimiTestStatusError{500, busy},
		kimiTestStatusError{403, `{"error":{"type":"access_terminated_error","message":"Account suspended"}}`},
		kimiTestStatusError{429, `{"error":{"type":"access_terminated_error","message":"Usage limit exceeded"}}`},
		kimiTestStatusError{400, `{"error":{"type":"invalid_authentication_error","message":"K3.1 Pro does not support disabled thinking mode."}}`},
		kimiTestStatusError{403, `{"error":{"type":"other_error","message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."}}`},
		kimiTestStatusError{http.StatusForbidden, `invalid JSON: ` + busy},
	}
	for _, original := range cases {
		if got := NormalizeKimiConcurrencyError(original); got != original {
			t.Fatalf("unexpected classification: %v -> %v", original, got)
		}
	}
}
