package helps

import (
	"math"
	"net/http"
	"testing"
	"time"
)

func TestParseRetryDelay_Issue7565Formats(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    time.Duration
		wantErr bool
	}{
		{
			name: "issue 7565 google rpc RetryInfo 2190s",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}`,
			want: 2190 * time.Second,
		},
		{
			name: "issue 7565 message Please retry in fractional duration",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted. Please retry in 36m41.110635822s."}}`,
			want: 36*time.Minute + 41110635822*time.Nanosecond,
		},
		{
			name: "message with earlier word after still finds retry in duration",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded after multiple requests. Please retry in 36m41.110635822s."}}`,
			want: 36*time.Minute + 41110635822*time.Nanosecond,
		},
		{
			name: "uppercase duration units in message are accepted",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota will reset after 1H2M3S."}}`,
			want: time.Hour + 2*time.Minute + 3*time.Second,
		},
		{
			name: "uppercase retry in duration units are accepted",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Please retry in 36M41.5S."}}`,
			want: 36*time.Minute + 41*time.Second + 500*time.Millisecond,
		},
		{
			name: "array wrapped google rpc RetryInfo",
			body: `[{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}]`,
			want: 2190 * time.Second,
		},
		{
			name: "malformed RetryInfo falls back to ErrorInfo quotaResetDelay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"not-a-duration"},{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetDelay":"120s"}}]}}`,
			want: 120 * time.Second,
		},
		{
			name: "zero RetryInfo preserved for legacy ParseRetryDelay callers",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`,
			want: 0,
		},
		{
			name: "zero quotaResetDelay preserved for legacy ParseRetryDelay callers",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetDelay":"0s"}}]}}`,
			want: 0,
		},
		{
			name: "zero message duration preserved for legacy ParseRetryDelay callers",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Your quota will reset after 0s."}}`,
			want: 0,
		},
		{
			name:    "negative RetryInfo without fallback fails",
			body:    `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"-5s"}]}}`,
			wantErr: true,
		},
		{
			name:    "word starting with unit letter after number does not false-match",
			body:    `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Failed after 3models or retry in 2steps."}}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errParse := ParseRetryDelay([]byte(tc.body))
			if tc.wantErr {
				if errParse == nil && got != nil {
					t.Fatalf("ParseRetryDelay() = %v, nil; want error", *got)
				}
				return
			}
			if errParse != nil {
				t.Fatalf("ParseRetryDelay() error = %v", errParse)
			}
			if got == nil || *got != tc.want {
				t.Fatalf("ParseRetryDelay() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseGoogleRetryAfter_PrecedenceAndHeaders(t *testing.T) {
	now := time.Date(2026, 11, 15, 15, 8, 53, 0, time.UTC)
	futureHTTPDate := now.Add(2190 * time.Second).UTC().Format(http.TimeFormat)
	pastHTTPDate := now.Add(-10 * time.Second).UTC().Format(http.TimeFormat)

	wantDuration := func(d time.Duration) *time.Duration {
		return &d
	}

	tests := []struct {
		name    string
		status  int
		headers http.Header
		body    string
		want    *time.Duration
	}{
		{
			name:    "non-429 ignores Retry-After and RetryInfo",
			status:  http.StatusServiceUnavailable,
			headers: http.Header{"Retry-After": []string{"2190"}},
			body:    `{"error":{"code":503,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}`,
			want:    nil,
		},
		{
			name:    "structured RetryInfo wins over Retry-After header and message",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"60"}},
			body:    `{"error":{"code":429,"message":"Please retry in 10s.","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "zero RetryInfo falls back to positive Retry-After header",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"2190"}},
			body:    `{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "zero RetryInfo alone yields nil for Gemini",
			status:  http.StatusTooManyRequests,
			headers: nil,
			body:    `{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`,
			want:    nil,
		},
		{
			name:    "Retry-After header wins over unstructured message text",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"2190"}},
			body:    `{"error":{"code":429,"message":"Please retry in 36m41.110635822s."}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "lowercase retry-after header key honored",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"retry-after": []string{"2190"}},
			body:    `{"error":{"code":429}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "future HTTP-date Retry-After honored",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{futureHTTPDate}},
			body:    `{"error":{"code":429}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "zero Retry-After without body hint yields nil",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"0"}},
			body:    `{"error":{"code":429}}`,
			want:    nil,
		},
		{
			name:    "past HTTP-date Retry-After falls back to uppercase message duration",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{pastHTTPDate}},
			body:    `{"error":{"code":429,"message":"Please retry in 1M30S."}}`,
			want:    wantDuration(90 * time.Second),
		},
		{
			name:    "invalid Retry-After yields nil when body has no hint",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"soon"}},
			body:    `{"error":{"code":429,"message":"Rate limited."}}`,
			want:    nil,
		},
		{
			name:    "huge Retry-After seconds clamps to MaxInt64",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"18446744074"}},
			body:    `{"error":{"code":429}}`,
			want:    wantDuration(time.Duration(math.MaxInt64)),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseGoogleRetryAfter(tc.status, tc.headers, []byte(tc.body), now)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("ParseGoogleRetryAfter() = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("ParseGoogleRetryAfter() = %v, want %v", got, *tc.want)
			}
		})
	}
}

func TestParseRetryDelay_Formats(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      time.Duration
		expectErr bool
	}{
		{
			name: "google errorinfo metadata camelCase quotaResetDelay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetDelay":"479.417207ms"}}]}}`,
			want: 479417207 * time.Nanosecond,
		},
		{
			name: "google errorinfo metadata snake_case quota_reset_delay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quota_reset_delay":"166h8m44s"}}]}}`,
			want: 166*time.Hour + 8*time.Minute + 44*time.Second,
		},
		{
			name: "google retryinfo retryDelay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0.5s"}]}}`,
			want: 500 * time.Millisecond,
		},
		{
			name: "message with resets in human duration",
			body: `{"error":{"code":429,"message":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 166h27m18s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 166*time.Hour + 27*time.Minute + 18*time.Second,
		},
		{
			name: "message with resets in seconds",
			body: `{"error":{"code":429,"message":"Quota exceeded. Resets in 45s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 45 * time.Second,
		},
		{
			name: "message with after human duration",
			body: `{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 1h43m56s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 1*time.Hour + 43*time.Minute + 56*time.Second,
		},
		{
			name: "message with after seconds",
			body: `{"error":{"code":429,"message":"Your quota will reset after 30s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 30 * time.Second,
		},
		{name: "overflow seconds", body: `{"error":{"message":"Resets in 9223372036854775807s."}}`, expectErr: true},
		{name: "explicit zero message", body: `{"error":{"message":"Resets in 0s."}}`, want: 0},
		{name: "negative metadata", body: `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quota_reset_delay":"-1h"}}]}}`, expectErr: true},
		{name: "uppercase reset", body: `{"error":{"message":"RESETS IN 432h."}}`, want: 432 * time.Hour},
		{
			name:      "unrelated error body returns error",
			body:      `{"error":{"code":500,"message":"Internal Server Error"}}`,
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRetryDelay([]byte(tc.body))
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || *got != tc.want {
				t.Fatalf("ParseRetryDelay() = %v, want %v", got, tc.want)
			}
		})
	}
}
