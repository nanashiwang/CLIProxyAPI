package logging

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestFormatCPATraceID(t *testing.T) {
	selectedAt := time.Date(2026, time.July, 17, 21, 58, 49, 0, time.UTC)
	got := FormatCPATraceID(selectedAt, "auth-index", "request1")
	if want := "20260717215849-auth-index-request1"; got != want {
		t.Fatalf("FormatCPATraceID() = %q, want %q", got, want)
	}

	for _, test := range []struct {
		name       string
		selectedAt time.Time
		authIndex  string
		requestID  string
	}{
		{name: "zero time", authIndex: "auth-index", requestID: "request1"},
		{name: "empty auth index", selectedAt: selectedAt, requestID: "request1"},
		{name: "empty request ID", selectedAt: selectedAt, authIndex: "auth-index"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if gotEmpty := FormatCPATraceID(test.selectedAt, test.authIndex, test.requestID); gotEmpty != "" {
				t.Fatalf("FormatCPATraceID() = %q, want empty", gotEmpty)
			}
		})
	}
}

func captureSelectionLogs(t *testing.T) *logtest.Hook {
	t.Helper()
	logger := log.StandardLogger()
	oldOutput, oldLevel := logger.Out, logger.GetLevel()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	logger.SetOutput(io.Discard)
	logger.SetLevel(log.InfoLevel)
	hook := logtest.NewGlobal()
	t.Cleanup(func() {
		logger.ReplaceHooks(oldHooks)
		logger.SetOutput(oldOutput)
		logger.SetLevel(oldLevel)
	})
	return hook
}

func TestCredentialSelectionLogsAtInfoWithNANID(t *testing.T) {
	hook := captureSelectionLogs(t)
	engine := gin.New()
	engine.Use(GinLogrusLogger(), CPATraceIDMiddleware())
	engine.POST("/v1/responses", func(c *gin.Context) {
		callback := GinCPATraceIDCallback(c)
		callback("index-a")
		c.Writer.WriteHeaderNow()
		// Later selections must still be logged after SSE headers are committed.
		callback("index-b")
		GinCPATraceIDCallback(c)("index-b")
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"prompt":"private-prompt"}`))
	request.Header.Set("X-NewAPI-Request-ID", "nan-request-123")
	request.Header.Set("Authorization", "Bearer private-token")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	var selected []*log.Entry
	var formatted bytes.Buffer
	for _, entry := range hook.AllEntries() {
		if entry.Message != "credential selected" {
			continue
		}
		selected = append(selected, entry)
		line, err := (&LogFormatter{}).Format(entry)
		if err != nil {
			t.Fatal(err)
		}
		formatted.Write(line)
	}
	if len(selected) != 3 {
		t.Fatalf("selection records = %d, want 3", len(selected))
	}
	for i, entry := range selected {
		if entry.Data["request_id"] != "nan-request-123" ||
			entry.Data["selection_seq"] != uint64(i+1) ||
			entry.Data["state"] != "selected" ||
			entry.Level != log.InfoLevel {
			t.Fatalf("unexpected selection record: %#v", entry)
		}
		if entry.Data["cpa_execution_id"] == "" ||
			entry.Data["cpa_execution_id"] != selected[0].Data["cpa_execution_id"] {
			t.Fatal("callbacks did not share execution ID")
		}
	}
	if selected[0].Data["auth_index"] != "index-a" || selected[1].Data["auth_index"] != "index-b" {
		t.Fatal("lost earlier selected account")
	}
	for _, want := range []string{"nan-request-123", `auth_index="index-a"`, "selection_seq=3", "cpa_execution_id="} {
		if !strings.Contains(formatted.String(), want) {
			t.Fatalf("missing %q in rendered logs", want)
		}
	}
	for _, secret := range []string{"private-token", "private-prompt", "Authorization"} {
		if strings.Contains(formatted.String(), secret) {
			t.Fatalf("logs exposed %q", secret)
		}
	}
	if !strings.Contains(recorder.Header().Get(CPATraceIDHeader), "-index-a-nan-request-123") {
		t.Fatal("changed response header semantics")
	}
}

func TestSelectionLogsConcurrentCallbacksAndSeparateExecutions(t *testing.T) {
	hook := captureSelectionLogs(t)
	newCallback := func() func(string) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		SetGinRequestID(c, "same-nan-id")
		return GinCPATraceIDCallback(c)
	}
	callback := newCallback()
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() { defer wg.Done(); callback("index-a") }()
	}
	wg.Wait()
	entries := hook.AllEntries()
	if len(entries) != 40 {
		t.Fatalf("records = %d", len(entries))
	}
	seqs := make(map[uint64]bool)
	for _, entry := range entries {
		seqs[entry.Data["selection_seq"].(uint64)] = true
	}
	if len(seqs) != 40 || !seqs[1] || !seqs[40] {
		t.Fatal("non-unique selection sequence")
	}
	newCallback()("index-b")
	last := hook.LastEntry()
	if last.Data["cpa_execution_id"] == entries[0].Data["cpa_execution_id"] {
		t.Fatal("NAN retries into the same pool must have distinct execution IDs")
	}
	if last.Data["selection_seq"] != uint64(1) {
		t.Fatal("new execution did not restart sequence")
	}
}

func TestSelectionLogsSkipMissingOrUnsafeIdentifiers(t *testing.T) {
	hook := captureSelectionLogs(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if GinCPATraceIDCallback(c) != nil {
		t.Fatal("missing request ID must not create callback")
	}
	SetGinRequestID(c, "nan-id")
	callback := GinCPATraceIDCallback(c)
	for _, value := range []string{"", "account@example.com", "bad\nindex", strings.Repeat("a", 129)} {
		callback(value)
	}
	if len(hook.AllEntries()) != 0 {
		t.Fatal("unsafe identifiers leaked into selection logs")
	}
}

func TestCPATraceIDMiddlewareRequiresAuthIndexBeforeResponseCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/selected", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		SetGinCPATraceID(c, "auth-index")
		c.Status(http.StatusOK)
	})
	engine.GET("/unselected", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		SetGinCPATraceID(c, "")
		c.Status(http.StatusOK)
	})
	engine.GET("/committed", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		c.Writer.WriteHeaderNow()
		SetGinCPATraceID(c, "auth-index")
	})

	t.Run("writes selected auth trace", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/selected", nil))

		traceID := recorder.Header().Get(CPATraceIDHeader)
		if len(traceID) != len("20060102150405-auth-index-1234abcd") {
			t.Fatalf("trace ID = %q, unexpected length", traceID)
		}
		if got := traceID[15:]; got != "auth-index-1234abcd" {
			t.Fatalf("trace suffix = %q, want %q", got, "auth-index-1234abcd")
		}
	})

	t.Run("skips empty auth index", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unselected", nil))

		if got := recorder.Header().Get(CPATraceIDHeader); got != "" {
			t.Fatalf("trace ID = %q, want empty", got)
		}
	})

	t.Run("skips committed response", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/committed", nil))

		if got := recorder.Header().Get(CPATraceIDHeader); got != "" {
			t.Fatalf("trace ID = %q, want empty", got)
		}
	})
}

func TestCPATraceIDConcurrentSelectionAndResponseCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/race", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		traceCallback := GinCPATraceIDCallback(c)
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-start
			traceCallback("auth-index")
		}()
		close(start)
		_, _ = c.Writer.Write([]byte("\n"))
		<-done
	})

	for range 100 {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/race", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}
}
