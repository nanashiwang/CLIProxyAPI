package helps

import "testing"

func TestCodexRetryMetadataRequiresStructuredString(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"headers":{"X-Retry-Metadata":" no_more_retry "}}`, true},
		{`{"response":{"error":{"headers":{"x-retry-metadata":"NO_MORE_RETRY"}}}}`, true},
		{`{"error":{"headers":{"x-retry-metadata":["NO_MORE_RETRY"]}}}`, false},
		{`{"error":{"headers":{"x-retry-metadata":true}}}`, false},
		{`{"error":{"headers":{"x-retry-metadata":"RETRY"}}}`, false},
		{`{"error":{"message":"x-retry-metadata: NO_MORE_RETRY"}}`, false},
		{`{"headers":{"x-retry-metadata":"NO_MORE_RETRY"}`, false},
	} {
		if got := CodexNoMoreRetry([]byte(tc.body)); got != tc.want {
			t.Fatalf("%s: %v", tc.body, got)
		}
	}
}
