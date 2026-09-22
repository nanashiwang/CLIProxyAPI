package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type authFilesCooldownResponse struct {
	Files []struct {
		ID             string    `json:"id"`
		AuthIndex      string    `json:"auth_index"`
		Name           string    `json:"name"`
		Status         string    `json:"status"`
		StatusMessage  string    `json:"status_message"`
		Unavailable    bool      `json:"unavailable"`
		NextRetryAfter time.Time `json:"next_retry_after"`
	} `json:"files"`
}

func requestAuthFilesCooldowns(t *testing.T, h *Handler, query string) authFilesCooldownResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files"+query, nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload authFilesCooldownResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	return payload
}

func TestListAuthFiles_ExpiredCooldownReconciledToActive(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Credential-level expired cooldown (e.g. Codex quota exhaustion in Issue 5964)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "codex-expired",
		Index:          "idx-codex-expired",
		FileName:       "codex-expired.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "credential_quota",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredDeadline,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	// Model-level expired cooldown where all model cooldowns have elapsed
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "model-expired",
		Index:          "idx-model-expired",
		FileName:       "model-expired.json",
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "rate limit exceeded",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		ModelStates: map[string]*coreauth.ModelState{
			"claude-3-5-sonnet": {
				Status:         coreauth.StatusError,
				StatusMessage:  "rate limit exceeded",
				Unavailable:    true,
				NextRetryAfter: expiredDeadline,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")

	if len(payload.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(payload.Files))
	}

	for _, file := range payload.Files {
		if file.Unavailable {
			t.Errorf("auth %s: expected unavailable=false after cooldown expiration, got true", file.ID)
		}
		if file.Status != string(coreauth.StatusActive) {
			t.Errorf("auth %s: expected status=%q after cooldown expiration, got %q", file.ID, coreauth.StatusActive, file.Status)
		}
		if file.StatusMessage != "" {
			t.Errorf("auth %s: expected empty status_message after cooldown expiration, got %q", file.ID, file.StatusMessage)
		}
		if !file.NextRetryAfter.IsZero() {
			t.Errorf("auth %s: expected zero next_retry_after after cooldown expiration, got %v", file.ID, file.NextRetryAfter)
		}
	}
}

func TestListAuthFiles_ExpiredCooldownWithSubsequentTokenFailure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth auth that had a cooldown, but subsequently had its access token expire / refresh fail.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "codex-token-expired",
		Index:          "idx-codex-token-expired",
		FileName:       "codex-token-expired.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "token expired",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "expired-access-token",
			"expired":      now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=codex-token-expired.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true for expired token failure, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q for expired token failure, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "token expired" {
		t.Errorf("expected status_message=%q, got %q", "token expired", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_PartialModelCooldownWithAuthFailure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Auth has an independent auth error ("unauthorized"), but only one of its models is in cooldown.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:            "auth-unauthorized-partial-model",
		Index:         "idx-auth-unauthorized",
		FileName:      "auth-unauthorized.json",
		Provider:      "claude",
		Status:        coreauth.StatusError,
		StatusMessage: "unauthorized",
		Unavailable:   true,
		LastError:     &coreauth.Error{HTTPStatus: 401, Message: "unauthorized"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-cool": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
			"model-free": {
				Status: coreauth.StatusActive,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-unauthorized.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true due to unauthorized error, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "unauthorized" {
		t.Errorf("expected status_message=%q, got %q", "unauthorized", file.StatusMessage)
	}
}

func TestListAuthFiles_UnexpiredTokenWithRefresh401_NoCooldown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	futureExpiry := now.Add(48 * time.Hour).Format(time.RFC3339)
	retryBackoff := now.Add(5 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential whose access token is still valid (+48h), but background refresh failed with 401
	// and scheduled a retry backoff. Local policy still treats the 401 as terminal.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:               "auth-valid-token-refresh-401",
		Index:            "idx-valid-token",
		FileName:         "auth-valid-token.json",
		Provider:         "codex",
		Status:           coreauth.StatusActive,
		Unavailable:      false,
		NextRefreshAfter: retryBackoff,
		LastError:        &coreauth.Error{HTTPStatus: 401, Message: "401 unauthorized on refresh"},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-valid-token.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable || file.Status != string(coreauth.StatusError) {
		t.Fatalf("local terminal 401 must remain blocked: %+v", file)
	}
}

func TestListAuthFiles_UnexpiredTokenWithRefresh401_ExpiredCooldown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	futureExpiry := now.Add(48 * time.Hour).Format(time.RFC3339)
	expiredCooldown := now.Add(-10 * time.Minute)
	retryBackoff := now.Add(5 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential with a valid access token (+48h) that previously entered quota cooldown (now expired),
	// and has a pending refresh retry after a 401 refresh error.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:               "auth-valid-token-expired-cooldown",
		Index:            "idx-valid-token-exp-cool",
		FileName:         "auth-valid-token-exp-cool.json",
		Provider:         "codex",
		Status:           coreauth.StatusError,
		StatusMessage:    "credential_quota",
		Unavailable:      true,
		NextRetryAfter:   expiredCooldown,
		NextRefreshAfter: retryBackoff,
		LastError:        &coreauth.Error{HTTPStatus: 401, Message: "401 unauthorized on refresh"},
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredCooldown,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-valid-token-exp-cool.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable || file.Status != string(coreauth.StatusError) {
		t.Fatalf("local terminal 401 must remain blocked: %+v", file)
	}
}

func TestListAuthFiles_ModelLevel403CooldownExpired(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth whose model failed with 403 (forbidden) and the conductor copied the error to auth-level,
	// but the model-level cooldown has now elapsed.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "auth-model-403-expired",
		Index:          "idx-model-403-exp",
		FileName:       "auth-model-403-exp.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "forbidden",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {
				Status:         coreauth.StatusError,
				StatusMessage:  "forbidden",
				Unavailable:    true,
				NextRetryAfter: expiredDeadline,
				LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-model-403-exp.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false after model-level 403 cooldown expires, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
	if file.StatusMessage != "" {
		t.Errorf("expected empty status_message after cooldown expires, got %q", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_SingleModel403Cooling_OtherModelActive(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth where Model A is in a 403 cooldown, but Model B is active and healthy.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:            "auth-single-403-other-active",
		Index:         "idx-single-403",
		FileName:      "auth-single-403.json",
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "forbidden",
		Unavailable:   false,
		LastError:     &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-forbidden": {
				Status:         coreauth.StatusError,
				StatusMessage:  "forbidden",
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
				LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
			},
			"model-working": {
				Status: coreauth.StatusActive,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-single-403.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false because model-working is active, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q because model-working is active, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_ExpiredCooldown_ExpiredToken_InFlightRefresh(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)
	inFlightRefresh := now.Add(1 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential whose quota cooldown expired, but its access token is expired,
	// and a background refresh is currently in-flight/scheduled (NextRefreshAfter in future).
	// Because the access token is expired, selector blocks it and it must NOT be reported as active.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:               "auth-expired-cooldown-expired-token",
		Index:            "idx-exp-cool-exp-tok",
		FileName:         "auth-exp-cool-exp-tok.json",
		Provider:         "codex",
		Status:           coreauth.StatusError,
		StatusMessage:    "credential_quota",
		Unavailable:      true,
		NextRetryAfter:   expiredDeadline,
		NextRefreshAfter: inFlightRefresh,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredDeadline,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "expired-access-token",
			"expired":      now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-exp-cool-exp-tok.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true because access token is expired, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "credential_quota" {
		t.Errorf("expected status_message=%q, got %q", "credential_quota", file.StatusMessage)
	}
}

func TestListAuthFiles_ActiveAuth_InactiveFutureTimestamp(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	inactiveFutureTimestamp := now.Add(1 * time.Hour)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An active, healthy auth that has an inactive future NextRetryAfter timestamp
	// (Unavailable=false, Quota.Exceeded=false). Matching selector.availabilityBlock,
	// an inactive timestamp does not block the credential or report it as in error.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "auth-active-inactive-future-timestamp",
		Index:          "idx-active-inactive-future",
		FileName:       "auth-active-future.json",
		Provider:       "codex",
		Status:         coreauth.StatusActive,
		Unavailable:    false,
		NextRetryAfter: inactiveFutureTimestamp,
		Attributes:     map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-active-future.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false for active auth with inactive future timestamp, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_ModelCooling_OtherModelPermanentBlocked(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Model A is in active cooldown (+30m).
	// Model B has a permanent failure with no recovery time (Unavailable=true, NextRetryAfter=zero).
	// Because all schedulable models are blocked, the credential as a whole cannot serve any model.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:            "auth-all-blocked-cooling-and-perm",
		Index:         "idx-all-blocked",
		FileName:      "auth-all-blocked.json",
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "model failure",
		Unavailable:   true,
		ModelStates: map[string]*coreauth.ModelState{
			"model-cooling": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
			"model-perm-blocked": {
				Status:      coreauth.StatusError,
				Unavailable: true,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-all-blocked.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true because all models are blocked, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q because all models are blocked, got %q", coreauth.StatusError, file.Status)
	}
}

func TestListAuthFiles_SparseModelState_BlockedModelDoesNotMakeAuthUnavailable(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth where Model A has an active cooldown in ModelStates, but Model B has no entry in the sparse map yet.
	// Because other supported models are schedulable and auth.Unavailable is false, the credential as a whole
	// must NOT be reported as unavailable.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:          "auth-sparse-models",
		Index:       "idx-sparse-models",
		FileName:    "auth-sparse-models.json",
		Provider:    "codex",
		Status:      coreauth.StatusActive,
		Unavailable: false,
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-sparse-models.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false because sparse unrecorded models remain schedulable, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestCooldownViewsConsistentAndReadOnly(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name    string
		quota   *coreauth.CodexQuotaSnapshot
		failure *coreauth.Error
		blocked bool
	}{
		{name: "expired"},
		{name: "exhausted", quota: &coreauth.CodexQuotaSnapshot{Exhausted: true}, blocked: true},
		{name: "unauthorized", failure: &coreauth.Error{HTTPStatus: 401}, blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			cfg := &config.Config{AuthDir: t.TempDir()}
			manager.SetConfig(cfg)
			registerAuthForLookupTest(t, manager, &coreauth.Auth{ID: "view", FileName: "view.json", Provider: "codex", Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(-time.Minute), LastError: tc.failure, CodexQuota: tc.quota, Attributes: map[string]string{"runtime_only": "true"}})
			h := NewHandlerWithoutConfigFilePath(cfg, manager)
			before, _ := manager.GetByID("view")
			file := requestAuthFilesCooldowns(t, h, "").Files[0]
			pool := getPoolView(t, h)
			var credentials []struct {
				Unavailable bool `json:"unavailable"`
			}
			if err := json.Unmarshal(pool["credentials"], &credentials); err != nil {
				t.Fatal(err)
			}
			if len(credentials) != 1 || credentials[0].Unavailable != tc.blocked || file.Unavailable != tc.blocked {
				t.Fatal("auth files and account pools disagree", file, credentials)
			}
			after, _ := manager.GetByID("view")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("GET mutated manager state")
			}
		})
	}
}

func TestCooldownViewsConcurrentReadsAndResults(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{ID: "concurrent", FileName: "concurrent.json", Provider: "codex", Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(-time.Minute), Attributes: map[string]string{"runtime_only": "true"}})
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	var wg sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func(writer bool) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				if writer {
					manager.MarkResult(context.Background(), coreauth.Result{AuthID: "concurrent", Model: "test-model", Success: i%2 == 0, Error: &coreauth.Error{HTTPStatus: 401, Message: "unauthorized"}})
					continue
				}
				files := requestAuthFilesCooldowns(t, h, "").Files
				if len(files) != 1 {
					t.Error("missing auth snapshot")
					return
				}
				if files[0].Unavailable && files[0].Status == string(coreauth.StatusActive) {
					t.Error("incoherent availability snapshot")
				}
				getPoolView(t, h)
			}
		}(worker == 0)
	}
	wg.Wait()
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "concurrent", Model: "test-model", Error: &coreauth.Error{HTTPStatus: 401, Message: "unauthorized"}})
	file := requestAuthFilesCooldowns(t, h, "").Files[0]
	if !file.Unavailable || file.Status != string(coreauth.StatusError) {
		t.Fatal("concurrent reads erased terminal failure", file)
	}
}

func TestQuotaCeilingManagementEndpointsRetainCooldown(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{ID: "ceiling", FileName: "ceiling.json", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"runtime_only": "true"}})
	retry := 18 * 24 * time.Hour
	start := time.Now()
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "ceiling", Model: "model-a", CredentialScope: true, RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: 429, Message: "quota exhausted"}})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			manager.MarkResult(context.Background(), coreauth.Result{AuthID: "ceiling", Model: "model-b", Success: true})
		}()
	}
	wg.Wait()
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	before, _ := manager.GetByID("ceiling")
	file := requestAuthFilesCooldowns(t, h, "").Files[0]
	var credentials []struct {
		Unavailable bool `json:"unavailable"`
	}
	if err := json.Unmarshal(getPoolView(t, h)["credentials"], &credentials); err != nil {
		t.Fatal(err)
	}
	if !file.Unavailable || file.Status != string(coreauth.StatusError) || file.NextRetryAfter.Before(start.Add(time.Hour)) || file.NextRetryAfter.After(time.Now().Add(time.Hour)) {
		t.Fatal("auth file lost bounded cooldown", file)
	}
	if len(credentials) != 1 || !credentials[0].Unavailable {
		t.Fatal("pool and auth file availability differs", credentials, file)
	}
	after, _ := manager.GetByID("ceiling")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("management read changed quota state")
	}
}
