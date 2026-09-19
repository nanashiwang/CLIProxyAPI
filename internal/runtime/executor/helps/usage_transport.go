package helps

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// ObserveHTTPResponseTransport records the upstream API protocol of an actual
// HTTP response, including HTTP responses carried inside a websocket relay.
func (r *UsageReporter) ObserveHTTPResponseTransport(headers http.Header) {
	if r == nil {
		return
	}
	r.modelMu.RLock()
	generation := r.modelGeneration
	r.modelMu.RUnlock()
	r.observeHTTPResponseTransport(headers, generation)
}

func (r *UsageReporter) observeHTTPResponseTransport(headers http.Header, generation uint64) {
	transport := "http"
	if mediaType := strings.SplitN(headers.Get("Content-Type"), ";", 2)[0]; strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream") {
		transport = "sse"
	}
	r.observeUpstreamTransport(transport, generation)
}

// ObserveUpstreamTransport records a transport actually used by a non-HTTP
// executor. Call after starting the exchange and immediately before sending.
// HTTP clients observe their request/response through TrackHTTPClient instead.
func (r *UsageReporter) ObserveUpstreamTransport(transport string) {
	if r == nil {
		return
	}
	r.modelMu.RLock()
	generation := r.modelGeneration
	r.modelMu.RUnlock()
	r.observeUpstreamTransport(transport, generation)
}

func (r *UsageReporter) observeUpstreamTransport(transport string, generation uint64) {
	transport = usage.NormalizeTransport(transport)
	if transport == "" {
		return
	}
	r.modelMu.Lock()
	defer r.modelMu.Unlock()
	if generation == r.modelGeneration {
		r.upstreamTransport = transport
	}
}
