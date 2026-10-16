package usage

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"syscall"
)

// FailureCategory follows wrapped causes without copying their text, URLs,
// request bodies or credential identifiers into persisted diagnostics.
func FailureCategory(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) {
		return "tls"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection_refused"
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return "connection_closed"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		switch code := status.StatusCode(); {
		case code == 401 || code == 403:
			return "authentication"
		case code == 429:
			return "rate_limited"
		case code >= 500:
			return "upstream_server"
		case code >= 400:
			return "invalid_request"
		}
	}
	return "unknown"
}

func normalizeFailureCategory(category string) string {
	switch category {
	case "", "cancelled", "timeout", "dns", "tls", "connection_refused", "connection_closed", "authentication", "rate_limited", "upstream_server", "invalid_request", "unknown":
		return category
	}
	return "unknown"
}
