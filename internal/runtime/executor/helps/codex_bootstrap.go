package helps

const CodexBootstrapMaxEvents = 16
const CodexBootstrapMaxBytes = 1024 * 1024

// CodexBootstrapBudget bounds retained handshake data without changing timeouts.
// A rejected addition is forwarded immediately; accepted data is never replayed.
type CodexBootstrapBudget struct{ events, bytes int }

func (b *CodexBootstrapBudget) Accept(chunks [][]byte) (bool, string) {
	if b.events >= CodexBootstrapMaxEvents {
		return false, "buffer_event_limit"
	}
	added := 0
	for _, chunk := range chunks {
		if len(chunk) > CodexBootstrapMaxBytes-b.bytes-added {
			return false, "buffer_byte_limit"
		}
		added += len(chunk)
	}
	b.events++
	b.bytes += added
	return true, ""
}
