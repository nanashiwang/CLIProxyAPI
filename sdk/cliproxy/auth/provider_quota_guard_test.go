package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type providerQuotaTestExecutor struct {
	*poolCaptureExecutor
	provider string
	payload  string
	calls    int
}

func (e *providerQuotaTestExecutor) Identifier() string { return e.provider }
func (e *providerQuotaTestExecutor) HttpRequest(_ context.Context, _ *Auth, r *http.Request) (*http.Response, error) {
	e.calls++
	if r.Method != http.MethodGet && !(e.provider == "antigravity" && r.Method == http.MethodPost) {
		return nil, fmt.Errorf("unexpected request")
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(e.payload))}, nil
}

func TestProviderQuotaParse(t *testing.T) {
	cases := []struct {
		provider, body string
		count          int
		used           float64
		seconds        int64
	}{
		{"claude", `{"five_hour":{"utilization":96},"seven_day":{"utilization":0}}`, 2, 96, 18000},
		{"claude", `{"limits":[{"kind":"session","percent":83},{"kind":"weekly_all","percent":56,"is_active":false},{"kind":"weekly_scoped","percent":19,"is_active":false,"scope":{"model":{"display_name":"Fable"}}}]}`, 3, 83, 18000},
		{"claude", `{"five_hour":null,"seven_day":{"utilization":null}}`, 0, 0, 0},
		{"claude", `{"limits":[{"kind":"weekly_scoped","is_active":true,"scope":{"model":{"display_name":"Fable"}},"percent":97}]}`, 1, 97, 604800},
		{"xai", `{"config":{"currentPeriod":{"type":"weekly","end":"2026-11-18T00:00:00Z"},"creditUsagePercent":98}}`, 1, 98, 604800},
		{"xai", `{"config":{"monthlyLimit":{"val":"1000"},"used":{"val":"970"}}}`, 1, 97, 30 * 86400},
		{"xai", `{"config":{"monthlyLimit":0,"used":12}}`, 0, 0, 0},
		{"xai", `{"active":true}`, 0, 0, 0},
		{"kimi", `{"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","remaining":"4"}}]}`, 1, 96, 18000},
		{"kimi", `{"usage":{"limit":100,"used":97}}`, 1, 97, 604800},
		{"kimi", `{"limits":[{"detail":{"limit":100}}]}`, 0, 0, 0},
		{"antigravity", `{"groups":[{"displayName":"Claude","buckets":[{"bucketId":"weekly","window":"weekly","remainingFraction":0.03}]}]}`, 1, 97, 604800},
		{"antigravity", `{"groups":[{"buckets":[{"window":"weekly","remainingFraction":null}]}]}`, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.provider+tc.body, func(t *testing.T) {
			got := parseProviderQuota(tc.provider, []byte(tc.body), time.Now())
			if len(got) != tc.count {
				t.Fatalf("%+v", got)
			}
			if tc.count > 0 && (got[0].UsedPercent != tc.used || got[0].WindowSeconds != tc.seconds) {
				t.Fatalf("%+v", got[0])
			}
		})
	}
}

func TestProviderQuotaRefreshPauseResumePersistence(t *testing.T) {
	for _, provider := range []string{"claude", "xai", "kimi", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			e := &providerQuotaTestExecutor{poolCaptureExecutor: &poolCaptureExecutor{}, provider: provider}
			m.RegisterExecutor(e)
			a, err := m.Register(context.Background(), &Auth{ID: provider, Provider: provider, Status: StatusActive, Metadata: map[string]any{"access_token": "fixture", "account_id": "account", "project_id": "project"}})
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(a.ID, provider, []*registry.ModelInfo{{ID: "model"}})
			defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
			payload := func(used int) string {
				switch provider {
				case "claude":
					return fmt.Sprintf(`{"five_hour":{"utilization":%d}}`, used)
				case "xai":
					return fmt.Sprintf(`{"config":{"creditUsagePercent":%d}}`, used)
				case "kimi":
					return fmt.Sprintf(`{"usage":{"limit":100,"used":%d}}`, used)
				default:
					return fmt.Sprintf(`{"groups":[{"buckets":[{"bucketId":"weekly","window":"weekly","remainingFraction":%f}]}]}`, float64(100-used)/100)
				}
			}
			e.payload = payload(99)
			now := time.Now()
			refreshQuotaAt(m, context.Background(), now)
			got, _ := m.GetByID(a.ID)
			blocked, _, _ := isAuthBlockedForModel(got, "model", now)
			if got.ProviderQuota == nil || !blocked || !isCredentialBlocked(got, 1, now) {
				t.Fatal("quota did not block", got)
			}
			store := NewFileCooldownStateStore(t.TempDir())
			m.SetCooldownStateStore(store)
			m.PersistCooldownStates(context.Background())
			restored := NewManager(nil, nil, nil)
			_, _ = restored.Register(context.Background(), a)
			restored.SetCooldownStateStore(store)
			if err := restored.RestoreCooldownStates(context.Background()); err != nil {
				t.Fatal(err)
			}
			saved, _ := restored.GetByID(a.ID)
			if !saved.codexQuotaBlocked {
				t.Fatal("restart lost guard")
			}
			e.payload = `{}`
			refreshQuotaAt(m, context.Background(), now.Add(time.Minute))
			got, _ = m.GetByID(a.ID)
			if !got.codexQuotaBlocked {
				t.Fatal("empty payload fabricated recovery")
			}
			enabled := false
			m.SetConfig(&config.Config{QuotaGuard: &config.CodexQuotaGuard{Enabled: &enabled}})
			got, _ = m.GetByID(a.ID)
			if got.codexQuotaBlocked {
				t.Fatal("common disable ignored")
			}
			m.SetConfig(&config.Config{DisableCooling: true})
			got, _ = m.GetByID(a.ID)
			if !got.codexQuotaBlocked {
				t.Fatal("cooling bypassed guard")
			}
			e.payload = payload(0)
			refreshQuotaAt(m, context.Background(), now.Add(2*time.Minute))
			got, _ = m.GetByID(a.ID)
			if got.codexQuotaBlocked {
				t.Fatal("fresh quota did not resume")
			}
		})
	}
}

func TestProviderQuotaRefreshFencesAndIndependentWindows(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, _ := m.Register(context.Background(), &Auth{ID: "claude", Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}})
	now := time.Now()
	windows := []CodexQuotaWindow{{Name: "short", UsedPercent: 99, WindowSeconds: 18000}, {Name: "weekly", UsedPercent: 99, WindowSeconds: 604800}}
	if !m.applyProviderQuotaRefresh(a, windows, now, now) {
		t.Fatal("initial refresh rejected")
	}
	a, _ = m.GetByID(a.ID)
	if !m.applyProviderQuotaRefresh(a, []CodexQuotaWindow{{Name: "short", UsedPercent: 0, WindowSeconds: 18000}}, now.Add(time.Second), now) {
		t.Fatal("partial refresh rejected")
	}
	a, _ = m.GetByID(a.ID)
	if !a.codexQuotaBlocked {
		t.Fatal("partial response cleared weekly stop")
	}
	if m.applyProviderQuotaRefresh(a, windows, now.Add(-time.Second), now) {
		t.Fatal("stale response accepted")
	}
	a.Metadata["access_token"] = "other"
	if m.applyProviderQuotaRefresh(a, windows, now.Add(2*time.Second), now) {
		t.Fatal("credential change accepted")
	}
}

func TestProviderQuotaRefreshBatchFairness(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&providerQuotaTestExecutor{provider: "claude"})
	for i := 0; i < 205; i++ {
		_, err := m.Register(context.Background(), &Auth{ID: fmt.Sprintf("%03d", i), Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	now := time.Now()
	for i := 0; i < 3; i++ {
		for _, job := range m.reserveCodexQuotaRefreshes(now.Add(time.Duration(i) * time.Minute)) {
			seen[job.auth.ID] = true
			job.attempt.inFlight = false
		}
	}
	if len(seen) != 205 {
		t.Fatal("quota polling starved accounts", len(seen))
	}
}

func TestProviderQuotaWindowIdentity(t *testing.T) {
	windows := parseProviderQuota("antigravity", []byte(`{"groups":[{"displayName":"Claude","buckets":[{"bucketId":"weekly","window":"weekly","remainingFraction":0.01}]},{"displayName":"Gemini","buckets":[{"bucketId":"weekly","window":"weekly","remainingFraction":1}]}]}`), time.Now())
	if len(windows) != 2 || windows[0].Name == windows[1].Name {
		t.Fatal("group scopes collided", windows)
	}
	old := parseProviderQuota("claude", []byte(`{"iguana_necktie":{"utilization":99}}`), time.Now())
	fresh := parseProviderQuota("claude", []byte(`{"limits":[{"kind":"weekly_scoped","is_active":true,"scope":{"model":{"display_name":"Fable 5"}},"percent":0}]}`), time.Now())
	if len(old) != 1 || len(fresh) != 1 || old[0].Name != fresh[0].Name {
		t.Fatal("Fable payload migration lost window identity")
	}
}
