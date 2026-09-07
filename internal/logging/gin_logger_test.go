package logging

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinLogrusLoggerValidatesIncomingCorrelationIDs(t *testing.T) {
	for _, tc := range []struct{ name, primary, alias, want string }{
		{"primary", " nan.123-ABC_456 ", "alias-id", "nan.123-ABC_456"},
		{"alias", "", "alias-id", "alias-id"},
		{"invalid primary", "bad\nid", "alias-id", "alias-id"},
		{"oversized", strings.Repeat("x", 129), "", ""},
		{"email", "account@example.com", "", ""},
		{"missing", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(GinLogrusLogger())
			engine.POST("/v1/responses", func(c *gin.Context) {
				got := GetGinRequestID(c)
				if tc.want != "" && got != tc.want {
					t.Fatalf("ID = %q, want %q", got, tc.want)
				}
				if !validCorrelationID(got) {
					t.Fatal("unsafe generated ID")
				}
				if tc.want == "" && len(got) != 8 {
					t.Fatalf("expected local fallback, got %q", got)
				}
				if GetRequestID(c.Request.Context()) != got {
					t.Fatal("context IDs disagree")
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request.Header.Set("X-NewAPI-Request-ID", tc.primary)
			request.Header.Set("X-NAN-REQUEST-ID", tc.alias)
			engine.ServeHTTP(httptest.NewRecorder(), request)
		})
	}
}

func TestGinLogrusRecoveryRepanicsErrAbortHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/abort", func(c *gin.Context) {
		panic(http.ErrAbortHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	recorder := httptest.NewRecorder()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic, got nil")
		}
		err, ok := recovered.(error)
		if !ok {
			t.Fatalf("expected error panic, got %T", recovered)
		}
		if !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("expected ErrAbortHandler, got %v", err)
		}
		if err != http.ErrAbortHandler {
			t.Fatalf("expected exact ErrAbortHandler sentinel, got %v", err)
		}
	}()

	engine.ServeHTTP(recorder, req)
}

func TestGinLogrusRecoveryHandlesRegularPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
}

func TestIsAIAPIPathIncludesPublicAPIGroups(t *testing.T) {
	for _, path := range []string{
		"/v1",
		"/v1/models",
		"/v1/alpha/search",
		"/v1beta/interactions",
		"/openai/v1/videos",
		"/backend-api/codex/responses",
	} {
		if !isAIAPIPath(path) {
			t.Fatalf("expected %s to be treated as AI API path", path)
		}
	}
	for _, path := range []string{
		"/v0/management/config",
		"/v10/models",
		"/openai/v10/videos",
		"/backend-api/codex-status",
	} {
		if isAIAPIPath(path) {
			t.Fatalf("expected %s not to be treated as AI API path", path)
		}
	}
}

func TestIsAIAPIPathIncludesImages(t *testing.T) {
	if !isAIAPIPath("/v1/images/generations") {
		t.Fatalf("expected /v1/images/generations to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/images/edits") {
		t.Fatalf("expected /v1/images/edits to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/videos") {
		t.Fatalf("expected /v1/videos to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/videos/video_123") {
		t.Fatalf("expected /v1/videos/video_123 to be treated as AI API path")
	}
	if !isAIAPIPath("/openai/v1/videos") {
		t.Fatalf("expected /openai/v1/videos to be treated as AI API path")
	}
	if !isAIAPIPath("/openai/v1/videos/video_123/content") {
		t.Fatalf("expected /openai/v1/videos/video_123/content to be treated as AI API path")
	}
}

func TestIsAIAPIPathIncludesCodexBackend(t *testing.T) {
	paths := []string{
		"/backend-api/codex/responses",
		"/backend-api/codex/responses/compact",
	}
	for _, path := range paths {
		if !isAIAPIPath(path) {
			t.Fatalf("expected %s to be treated as AI API path", path)
		}
	}
	if isAIAPIPath("/backend-api/codex-status") {
		t.Fatalf("expected /backend-api/codex-status not to be treated as AI API path")
	}
}

func TestGinLogrusLoggerAddsRequestIDForCodexBackend(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusLogger())

	var requestIDFromContext string
	var requestIDFromGin string
	engine.POST("/backend-api/codex/responses", func(c *gin.Context) {
		requestIDFromContext = GetRequestID(c.Request.Context())
		requestIDFromGin = GetGinRequestID(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if requestIDFromContext == "" {
		t.Fatalf("expected request ID in request context")
	}
	if requestIDFromGin != requestIDFromContext {
		t.Fatalf("expected Gin request ID %q to match context request ID %q", requestIDFromGin, requestIDFromContext)
	}
}

func TestGinLogrusLoggerReusesNANRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.POST("/v1/responses", func(c *gin.Context) {
		if got := GetGinRequestID(c); got != "nan-request-123" {
			t.Fatalf("request ID = %q, want %q", got, "nan-request-123")
		}
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("X-NewAPI-Request-ID", "nan-request-123")
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}
