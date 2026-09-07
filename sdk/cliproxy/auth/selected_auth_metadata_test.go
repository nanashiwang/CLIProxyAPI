package auth

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestManagerRetriesLogSelectedAccountIndices(t *testing.T) {
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
	for _, stream := range []bool{false, true} {
		m, executor := newCredentialRetryLimitTestManager(t, 0)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		logging.SetGinRequestID(c, "nan-retry-test")
		opts := cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: logging.GinCPATraceIDCallback(c),
		}}
		hook.Reset()
		var err error
		if stream {
			_, err = m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "test-model"}, opts)
		} else {
			_, err = m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "test-model"}, opts)
		}
		if err == nil || executor.Calls() != 2 {
			t.Fatalf("fixture must try two failing accounts, calls=%d err=%v", executor.Calls(), err)
		}
		indices := make(map[string]bool)
		count := 0
		for _, entry := range hook.AllEntries() {
			if entry.Message != "credential selected" {
				continue
			}
			count++
			if entry.Data["request_id"] != "nan-retry-test" || entry.Data["state"] != "selected" {
				t.Fatalf("unexpected selection metadata: %#v", entry.Data)
			}
			index := entry.Data["auth_index"].(string)
			indices[index] = true
		}
		if count != 2 || len(indices) != 2 {
			t.Fatalf("stream=%v: logged %d selections / %d accounts, want 2/2", stream, count, len(indices))
		}
	}
}

func TestPublishSelectedAuthMetadataIncludesStableIndex(t *testing.T) {
	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		FileName: "auth-1.json",
	}
	selectedAuthID := ""
	selectedAuthIndex := ""
	meta := map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			selectedAuthID = authID
		},
		cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(authIndex string) {
			selectedAuthIndex = authIndex
		},
	}

	publishSelectedAuthMetadata(meta, auth)

	if selectedAuthID != auth.ID {
		t.Fatalf("selected auth ID = %q, want %q", selectedAuthID, auth.ID)
	}
	if selectedAuthIndex == "" || selectedAuthIndex != auth.Index {
		t.Fatalf("selected auth index = %q, want %q", selectedAuthIndex, auth.Index)
	}
	if got := meta[cliproxyexecutor.SelectedAuthMetadataKey]; got != auth.ID {
		t.Fatalf("selected auth metadata = %#v, want %q", got, auth.ID)
	}
	if got := meta[cliproxyexecutor.SelectedAuthIndexMetadataKey]; got != auth.Index {
		t.Fatalf("selected auth index metadata = %#v, want %q", got, auth.Index)
	}
}
