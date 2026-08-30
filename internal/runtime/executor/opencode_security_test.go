package executor

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/opencode"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func openCodeTestAuth(tier, base, protocol string) *cliproxyauth.Auth {
	attrs := map[string]string{"tier": tier, "base_url": base, "api_key": "secret"}
	if protocol != "" {
		attrs["protocol"] = protocol
	}
	return &cliproxyauth.Auth{Provider: openCodeProvider, Attributes: attrs}
}

func TestValidateOpenCodeRequestBindsTierAndPath(t *testing.T) {
	tests := []struct {
		name     string
		auth     *cliproxyauth.Auth
		rawURL   string
		method   string
		wantCode int
	}{
		{name: "zen chat", auth: openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, ""), rawURL: config.DefaultOpenCodeZenURL + "/v1/chat/completions", method: http.MethodPost},
		{name: "go responses", auth: openCodeTestAuth("go", config.DefaultOpenCodeGoURL, "responses"), rawURL: config.DefaultOpenCodeGoURL + "/v1/responses", method: http.MethodPost},
		{name: "zen cannot use go endpoint", auth: openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, ""), rawURL: config.DefaultOpenCodeGoURL + "/v1/chat/completions", method: http.MethodPost, wantCode: http.StatusBadRequest},
		{name: "query rejected", auth: openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, ""), rawURL: config.DefaultOpenCodeZenURL + "/v1/chat/completions?x=1", method: http.MethodPost, wantCode: http.StatusBadRequest},
		{name: "userinfo rejected", auth: openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, ""), rawURL: "https://user:pass@opencode.ai/zen/v1/chat/completions", method: http.MethodPost, wantCode: http.StatusBadRequest},
		{name: "get rejected", auth: openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, ""), rawURL: config.DefaultOpenCodeZenURL + "/v1/chat/completions", method: http.MethodGet, wantCode: http.StatusMethodNotAllowed},
		{name: "protocol mismatch", auth: openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, "anthropic"), rawURL: config.DefaultOpenCodeZenURL + "/v1/chat/completions", method: http.MethodPost, wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.rawURL, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			_, gotErr := validateOpenCodeRequest(req, tt.auth)
			if tt.wantCode == 0 {
				if gotErr != nil {
					t.Fatalf("validateOpenCodeRequest() error = %v", gotErr)
				}
				return
			}
			if gotErr == nil {
				t.Fatal("validateOpenCodeRequest() unexpectedly succeeded")
			}
			status, ok := gotErr.(interface{ StatusCode() int })
			if !ok || status.StatusCode() != tt.wantCode {
				t.Fatalf("error status = %v, want %d", gotErr, tt.wantCode)
			}
		})
	}
}

func TestApplySafeOpenCodeCustomHeadersCannotOverrideSecurityHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, config.DefaultOpenCodeZenURL+"/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	auth := openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, "")
	auth.Attributes["header:Authorization"] = "Bearer attacker"
	auth.Attributes["header:Host"] = "attacker.example"
	auth.Attributes["header:X-Opencode-Project"] = "attacker-project"
	auth.Attributes["header:X-Trace-Id"] = "trace-123"
	auth.Attributes["header:X-Bad"] = "line1\nline2"
	auth.Attributes["header:Proxy-Authorization"] = "Basic attacker"
	req.Header.Set("Proxy-Authorization", "Basic inbound")
	req.Header.Set("Connection", "keep-alive")
	sanitizeOpenCodeTransportHeaders(req.Header)
	applyOpenCodeHeaders(req, auth, opencode.ProtocolChat, nil, nil)
	if got := req.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Host"); got != "" {
		t.Fatalf("Host = %q", got)
	}
	if got := req.Header.Get("X-Opencode-Project"); got != "opencode:default-project" {
		t.Fatalf("X-Opencode-Project = %q", got)
	}
	if got := req.Header.Get("X-Trace-Id"); got != "trace-123" {
		t.Fatalf("X-Trace-Id = %q", got)
	}
	if got := req.Header.Get("X-Bad"); got != "" {
		t.Fatalf("X-Bad = %q", got)
	}
	if got := req.Header.Get("Proxy-Authorization"); got != "" || req.Header.Get("Connection") != "" {
		t.Fatalf("unsafe transport headers were retained: proxy=%q connection=%q", got, req.Header.Get("Connection"))
	}
}

func TestParseOpenCodeRetryAfter(t *testing.T) {
	now := time.Now()
	if got := parseOpenCodeRetryAfter("60", now); got == nil || *got != time.Minute {
		t.Fatalf("numeric Retry-After = %v, want 1m", got)
	}
	if got := parseOpenCodeRetryAfter("999999999999", now); got == nil || *got != 7*24*time.Hour {
		t.Fatalf("bounded Retry-After = %v, want 7d", got)
	}
	if got := parseOpenCodeRetryAfter("invalid", now); got != nil {
		t.Fatalf("invalid Retry-After = %v", got)
	}
	if got := parseOpenCodeRetryAfter(now.Add(-24*time.Hour).Format(http.TimeFormat), now); got != nil {
		t.Fatalf("past Retry-After = %v", got)
	}
}

func TestNewOpenCodeStatusErrCarriesRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	resp.Header.Set("Retry-After", "12")
	err := newOpenCodeStatusErr(resp, []byte(`{"error":"busy"}`))
	if err.RetryAfter() == nil || *err.RetryAfter() != 12*time.Second {
		t.Fatalf("RetryAfter() = %v", err.RetryAfter())
	}
}

func TestValidateOpenCodeRequestRejectsRawPath(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, config.DefaultOpenCodeZenURL+"/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.URL.RawPath = "/zen%2Fv1/chat/completions"
	_, err = validateOpenCodeRequest(req, openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, ""))
	if err == nil {
		t.Fatal("validateOpenCodeRequest() accepted URL with RawPath")
	}
}

func TestNewOpenCodeHTTPClientDoesNotFollowRedirects(t *testing.T) {
	client := newOpenCodeHTTPClient(nil, nil, nil)
	req, err := http.NewRequest(http.MethodGet, "https://attacker.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(req, nil); err != http.ErrUseLastResponse {
		t.Fatalf("CheckRedirect() = %v, want ErrUseLastResponse", err)
	}
}

func TestOpenCodeResponseBodyErrorCoversAllProtocols(t *testing.T) {
	tests := []struct {
		name     string
		protocol opencode.Protocol
		body     string
	}{
		{name: "chat error", protocol: opencode.ProtocolChat, body: `{"error":{"message":"chat failed"}}`},
		{name: "anthropic error", protocol: opencode.ProtocolAnthropic, body: `{"type":"error","error":{"message":"anthropic failed"}}`},
		{name: "responses failed status", protocol: opencode.ProtocolResponses, body: `{"status":"failed","error":{"message":"responses failed"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err, ok := openCodeResponseBodyError(tt.protocol, []byte(tt.body))
			if !ok || err.StatusCode() < http.StatusBadRequest {
				t.Fatalf("openCodeResponseBodyError() = %#v, %v", err, ok)
			}
		})
	}
}

func TestProtocolFormatUsesCodexForResponses(t *testing.T) {
	if got := protocolFormat(opencode.ProtocolResponses); got != sdktranslator.FormatCodex {
		t.Fatalf("protocolFormat(responses) = %q, want %q", got, sdktranslator.FormatCodex)
	}
}

func TestOpenCodePrepareRequestRejectsUntrustedDestination(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://attacker.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	auth := openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, "")
	if err := NewOpenCodeExecutor(&config.Config{}).PrepareRequest(req, auth); err == nil {
		t.Fatal("PrepareRequest() injected OpenCode credentials into an untrusted destination")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q after rejected request", got)
	}
}

func TestOpenCodeErrorMessageIsBounded(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header)}
	err := newOpenCodeStatusErr(resp, []byte(`{"error":"`+strings.Repeat("x", maxOpenCodeErrorMessageBytes*2)+`"}`))
	if len(err.Error()) > maxOpenCodeErrorMessageBytes {
		t.Fatalf("error message length = %d, want <= %d", len(err.Error()), maxOpenCodeErrorMessageBytes)
	}
	if !strings.HasSuffix(err.Error(), "…") {
		t.Fatalf("bounded error message does not carry truncation marker")
	}
}

func TestOpenCodePrepareRequestInitializesNilHeaders(t *testing.T) {
	u, err := url.Parse(config.DefaultOpenCodeZenURL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{Method: http.MethodPost, URL: u, Host: "attacker.example"}
	if err := NewOpenCodeExecutor(&config.Config{}).PrepareRequest(req, openCodeTestAuth("zen", config.DefaultOpenCodeZenURL, "")); err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if req.Header == nil || req.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("PrepareRequest() did not initialize/authenticate headers: %#v", req.Header)
	}
	if req.Host != "" {
		t.Fatalf("PrepareRequest() retained an untrusted request Host: %q", req.Host)
	}
}

func TestOpenCodeCountTokensAcceptsNilContext(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("CountTokens() panicked with nil context: %v", recovered)
		}
	}()
	_, _ = NewOpenCodeExecutor(&config.Config{}).CountTokens(nil, nil, cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
}
