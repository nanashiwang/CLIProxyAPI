package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func refreshQuotaAt(m *Manager, ctx context.Context, now time.Time) {
	m.quotaHTTP.now = func() time.Time { return now }
	m.refreshCodexQuotas(ctx, now)
}
func TestQuotaHTTPSharedCooldownAndCache(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "a", Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}}
	now := time.Now()
	m.quotaHTTP.now = func() time.Time { return now }
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	count := 0
	fetch := func() (*http.Response, error) {
		count++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"600"}}, Body: io.NopCloser(strings.NewReader("limited"))}, nil
	}
	r, _ := m.QuotaHTTPRequest(context.Background(), a, req, fetch)
	r.Body.Close()
	if count != 1 || !m.quotaHTTPCooldown(a).Equal(now.Add(10*time.Minute)) {
		t.Fatal("Retry-After ignored")
	}
	now = now.Add(9 * time.Minute)
	r, _ = m.QuotaHTTPRequest(context.Background(), a, req, fetch)
	r.Body.Close()
	if count != 1 {
		t.Fatal("cooldown bypassed")
	}
	now = now.Add(2 * time.Minute)
	fetch = func() (*http.Response, error) {
		count++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":5}}`))}, nil
	}
	r, _ = m.QuotaHTTPRequest(context.Background(), a, req, fetch)
	r.Body.Close()
	r, _ = m.QuotaHTTPRequest(context.Background(), a, req, fetch)
	r.Body.Close()
	if count != 2 {
		t.Fatal("cache missed")
	}
}
func TestQuotaHTTPConcurrentDeduplication(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "a", Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}}
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	var count atomic.Int32
	var wg sync.WaitGroup
	fetch := func() (*http.Response, error) {
		count.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := m.QuotaHTTPRequest(context.Background(), a, req, fetch)
			if e != nil {
				t.Error(e)
				return
			}
			r.Body.Close()
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal(count.Load())
	}
}
func TestQuotaRetryAfterDateAndBackoff(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if !quotaRetryAfter(now.Add(time.Hour).Format(http.TimeFormat), now).Equal(now.Add(time.Hour)) {
		t.Fatal("date ignored")
	}
	for i := 1; i <= 8; i++ {
		d := quotaBackoff(i)
		if d < 2*time.Minute || d > 36*time.Minute {
			t.Fatal(d)
		}
	}
}

func TestQuotaHTTPStoppedWindowWaitAndCachedAge(t *testing.T) {
	m, e, a := newQuotaTestManager(t)
	now := time.Now()
	e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
		return quotaResponse(99, now.Add(3*24*time.Hour), true), nil
	}
	refreshQuotaAt(m, context.Background(), now)
	live, _ := m.GetByID(a.ID)
	if live.QuotaGuard.NextCheckAt.Before(now.Add(3 * 24 * time.Hour)) {
		t.Fatal("reset deadline missing")
	}
	refreshQuotaAt(m, context.Background(), now.Add(48*time.Hour))
	if e.calls.Load() != 1 {
		t.Fatal("queried stopped account before reset")
	}
	req, _ := http.NewRequest("GET", "https://chatgpt.com/backend-api/wham/usage", nil)
	r, err := m.QuotaHTTPRequest(context.Background(), live, req, func() (*http.Response, error) { t.Fatal("manual refresh bypassed reset sleep"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if !quotaObservedAt(r, now.Add(48*time.Hour)).Equal(now) {
		t.Fatal("cached observation was made fresh")
	}
	e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
		return quotaResponse(0, now.Add(4*24*time.Hour), true), nil
	}
	refreshQuotaAt(m, context.Background(), now.Add(3*24*time.Hour+codexQuotaResetGrace))
	live, _ = m.GetByID(a.ID)
	if live.codexQuotaBlocked || e.calls.Load() != 2 {
		t.Fatal("reset verification did not restore account")
	}
}
func TestQuotaHTTPManagementFailureProtectsAccount(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	now := time.Now()
	m.applyCodexQuotaRefresh(a, []CodexQuotaWindow{quotaWindow("primary", 1, now.Add(time.Hour))}, false, true, now, now)
	live, _ := m.GetByID(a.ID)
	m.RecordQuotaReadFailure(context.Background(), live, 429)
	live, _ = m.GetByID(a.ID)
	if !live.codexQuotaBlocked || live.QuotaGuard.HTTPStatus != 429 {
		t.Fatal("management failure did not protect routing")
	}
}
func TestQuotaHTTPCredentialAndAccountIsolation(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "a", Provider: "claude", Metadata: map[string]any{"access_token": "first"}}
	b := a.Clone()
	b.ID = "b"
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	count := 0
	fetch := func() (*http.Response, error) {
		count++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	for _, account := range []*Auth{a, b} {
		r, _ := m.QuotaHTTPRequest(context.Background(), account, req, fetch)
		r.Body.Close()
	}
	if count != 1 {
		t.Fatal("duplicate token did not share read")
	}
	b.Metadata["access_token"] = "replacement"
	r, _ := m.QuotaHTTPRequest(context.Background(), b, req, fetch)
	r.Body.Close()
	if count != 2 {
		t.Fatal("replaced credential reused old cache")
	}
	apiKey := &Auth{Provider: "claude", Attributes: map[string]string{"api_key": "private"}}
	if IsQuotaRead(apiKey, req) {
		t.Fatal("unsupported API keys share an empty identity")
	}
}

func TestQuotaHTTPGlobalConcurrencyBound(t *testing.T) {
	m := NewManager(nil, nil, nil)
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	fetch := func() (*http.Response, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := &Auth{ID: fmt.Sprint(i), Provider: "claude", Metadata: map[string]any{"access_token": fmt.Sprint(i)}}
			req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
			r, e := m.QuotaHTTPRequest(context.Background(), a, req, fetch)
			if e != nil {
				t.Error(e)
				return
			}
			r.Body.Close()
		}(i)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers not admitted")
		}
	}
	if peak.Load() > 4 {
		t.Fatal("shared concurrency exceeded")
	}
	close(release)
	wg.Wait()
	if peak.Load() > 4 {
		t.Fatal("shared concurrency exceeded")
	}
}
