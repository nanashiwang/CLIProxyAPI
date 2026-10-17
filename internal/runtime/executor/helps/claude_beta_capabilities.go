package helps

import "strings"

// MergeClaudeBetaCapabilities keeps all caller-requested protocol capabilities,
// independently of credential or client fingerprint recognition. Values remain
// case-sensitive; whitespace, empty entries and duplicates are removed.
func MergeClaudeBetaCapabilities(baseline, incoming string, extra []string) string {
	seen := make(map[string]struct{})
	values := make([]string, 0)
	appendValues := func(line string) {
		for _, value := range strings.Split(line, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			values = append(values, value)
		}
	}
	appendValues(baseline)
	appendValues(incoming)
	for _, line := range extra {
		appendValues(line)
	}
	return strings.Join(values, ",")
}
