package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

// CodexNoMoreRetry accepts only structured error metadata, never message text.
func CodexNoMoreRetry(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	for _, path := range []string{"error.headers", "response.error.headers", "body.error.headers", "headers"} {
		headers := gjson.GetBytes(body, path)
		blocked := false
		if headers.IsObject() {
			headers.ForEach(func(k, v gjson.Result) bool {
				if strings.EqualFold(k.String(), "x-retry-metadata") && v.Type == gjson.String && strings.EqualFold(strings.TrimSpace(v.String()), "NO_MORE_RETRY") {
					blocked = true
				}
				return true
			})
		}
		if blocked {
			return true
		}
	}
	return false
}
