package helps

import (
	"context"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"net/http"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestParseCodexQuotaEventHeadersPreservesActiveLimit(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"plan_type":"pro",
		"metered_limit_name":"codex_bengalfox",
		"rate_limits":{
			"primary":{"used_percent":2,"window_minutes":10080,"reset_at":1782951970},
			"secondary":null
		}
	}`))
	if headers.Get("X-Codex-Active-Limit") != "codex_bengalfox" ||
		headers.Get("X-Codex-Primary-Used-Percent") != "2" ||
		headers.Get("X-Codex-Primary-Window-Minutes") != "10080" ||
		headers.Get("X-Codex-Primary-Reset-At") != "1782951970" ||
		headers.Get("X-Codex-Plan-Type") != "pro" {
		t.Fatalf("unexpected quota headers: %#v", headers)
	}
}

func TestParseCodexQuotaEventHeadersDefaultsToCodexAndRejectsIncompleteWindows(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"rate_limits":{"primary":{"used_percent":42,"window_minutes":300,"reset_after_seconds":60}}
	}`))
	if headers.Get("X-Codex-Active-Limit") != "codex" || headers.Get("X-Codex-Primary-Reset-After-Seconds") != "60" {
		t.Fatalf("unexpected default quota headers: %#v", headers)
	}
	if got := ParseCodexQuotaEventHeaders([]byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":42}}}`)); got != nil {
		t.Fatalf("incomplete quota event produced headers: %#v", got)
	}

	headers = ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"rate_limits":{
			"primary":{"used_percent":42,"window_minutes":300},
			"secondary":{"used_percent":17,"window_minutes":10080,"reset_after_seconds":60}
		}
	}`))
	if headers.Get("X-Codex-Primary-Used-Percent") != "" ||
		headers.Get("X-Codex-Secondary-Used-Percent") != "17" {
		t.Fatalf("partially valid quota event produced incomplete headers: %#v", headers)
	}
}

func TestUsageReporterMergesWebsocketQuotaHeaders(t *testing.T) {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"X-Request-Id": []string{"req-1"}})
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.3-codex-spark", nil)
	reporter.ObserveQuotaHeaders(http.Header{
		"X-Codex-Active-Limit":           []string{"codex_bengalfox"},
		"X-Codex-Primary-Used-Percent":   []string{"2"},
		"X-Codex-Primary-Window-Minutes": []string{"10080"},
		"X-Codex-Primary-Reset-At":       []string{"1782951970"},
	})
	headers := reporter.responseHeaders(ctx)
	if headers.Get("X-Request-Id") != "req-1" || headers.Get("X-Codex-Active-Limit") != "codex_bengalfox" {
		t.Fatalf("merged response headers = %#v", headers)
	}
}

type quotaObservationExecutor struct{}

func (*quotaObservationExecutor) Identifier() string { return "codex" }
func (*quotaObservationExecutor) Execute(ctx context.Context, a *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	reporter := NewUsageReporter(ctx, "codex", "quota-observation-model", a)
	reporter.ObserveResponse(&http.Response{Header: http.Header{"X-Codex-Primary-Used-Percent": {"71"}, "X-Codex-Primary-Window-Minutes": {"300"}, "X-Codex-Primary-Reset-At": {"100"}}})
	reporter.ObserveQuotaHeaders(ParseCodexQuotaEventHeaders([]byte(`{"type":"codex.rate_limits","rate_limits":{"secondary":{"used_percent":84,"window_minutes":10080,"reset_at":100}}}`)))
	return coreexecutor.Response{}, nil
}
func (e *quotaObservationExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	_, err := e.Execute(ctx, a, r, o)
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Payload: []byte("data")}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, err
}
func (*quotaObservationExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (e *quotaObservationExecutor) CountTokens(ctx context.Context, a *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (*quotaObservationExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected quota probe")
}

func TestCodexQuotaReporterReachesLocalManagerDuringExecution(t *testing.T) {
	for _, stream := range []bool{false, true} {
		m := coreauth.NewManager(nil, nil, nil)
		m.SetRetryConfig(0, 0, 0)
		e := &quotaObservationExecutor{}
		m.RegisterExecutor(e)
		id := fmt.Sprintf("quota-reporter-%v", stream)
		_, err := m.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Metadata: map[string]any{"access_token": "test-token"}})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "quota-observation-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		req := coreexecutor.Request{Model: "quota-observation-model"}
		if stream {
			result, err := m.ExecuteStream(context.Background(), []string{"codex"}, req, coreexecutor.Options{})
			if err != nil {
				t.Fatal(err)
			}
			for range result.Chunks {
			}
		} else {
			if _, err := m.Execute(context.Background(), []string{"codex"}, req, coreexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
		}
		a, _ := m.GetByID(id)
		if a.CodexQuota == nil || len(a.CodexQuota.Windows) != 2 || a.CodexQuota.Windows[0].UsedPercent != 71 || a.CodexQuota.Windows[1].UsedPercent != 84 {
			t.Fatalf("observations did not reach manager: %+v", a.CodexQuota)
		}
	}
}
