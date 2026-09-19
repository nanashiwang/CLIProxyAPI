package usage

import (
	"context"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ClientRequestMetadata is an immutable request-entry snapshot. Transport is the
// selected downstream mode, including when a streaming request later fails.
type ClientRequestMetadata struct {
	Transport string
	ClientIP  string
	UserAgent string
}

type clientRequestMetadataContextKey struct{}

// NormalizeTransport leaves unobserved or unsupported transports unknown.
func NormalizeTransport(transport string) string {
	switch transport {
	case "http", "sse", "ws":
		return transport
	default:
		return ""
	}
}

// NormalizeClientIP accepts an address already resolved by the request entry.
// Forwarding headers and host:port values are not interpreted here.
func NormalizeClientIP(value string) string {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return address.WithZone("").Unmap().String()
}

// NormalizeUserAgent removes control/format characters and caps the UTF-8 value
// at 512 bytes without retaining an invalid or partial encoded character.
func NormalizeUserAgent(value string) string {
	var out strings.Builder
	out.Grow(min(len(value), 512))
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) || char == utf8.RuneError {
			continue
		}
		if out.Len()+utf8.RuneLen(char) > 512 {
			break
		}
		out.WriteRune(char)
	}
	return strings.TrimSpace(out.String())
}

// WithClientRequestMetadata stores a value copy, never a live HTTP/Gin request.
func WithClientRequestMetadata(ctx context.Context, metadata ClientRequestMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata.Transport = NormalizeTransport(metadata.Transport)
	metadata.ClientIP = NormalizeClientIP(metadata.ClientIP)
	metadata.UserAgent = NormalizeUserAgent(metadata.UserAgent)
	return context.WithValue(ctx, clientRequestMetadataContextKey{}, metadata)
}

// ClientRequestMetadataFromContext returns the captured value, or unknown fields
// for direct SDK callers that did not supply a request-entry snapshot.
func ClientRequestMetadataFromContext(ctx context.Context) ClientRequestMetadata {
	if ctx == nil {
		return ClientRequestMetadata{}
	}
	metadata, _ := ctx.Value(clientRequestMetadataContextKey{}).(ClientRequestMetadata)
	return metadata
}

// WithClientTransport refines an existing request-entry snapshot. It does not
// infer that a direct SDK invocation has an HTTP client merely because it streams.
func WithClientTransport(ctx context.Context, transport string) context.Context {
	if ctx == nil {
		return ctx
	}
	metadata, ok := ctx.Value(clientRequestMetadataContextKey{}).(ClientRequestMetadata)
	if !ok {
		return ctx
	}
	metadata.Transport = NormalizeTransport(transport)
	return WithClientRequestMetadata(ctx, metadata)
}

func normalizeRecordRequestMetadata(ctx context.Context, record *Record) {
	metadata := ClientRequestMetadataFromContext(ctx)
	if record.ClientTransport == "" {
		record.ClientTransport = metadata.Transport
	}
	if record.ClientIP == "" {
		record.ClientIP = metadata.ClientIP
	}
	if record.UserAgent == "" {
		record.UserAgent = metadata.UserAgent
	}
	record.ClientTransport = NormalizeTransport(record.ClientTransport)
	record.UpstreamTransport = NormalizeTransport(record.UpstreamTransport)
	record.ClientIP = NormalizeClientIP(record.ClientIP)
	record.UserAgent = NormalizeUserAgent(record.UserAgent)
}
