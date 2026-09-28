package management

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthFileCapacityBatchBoundsAndUnknown(t *testing.T) {
	m := auth.NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &auth.Auth{ID: "known", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{authManager: m}
	run := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/auth-files/capacity", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.GetAuthFileCapacity(c)
		return w
	}
	w := run(`{"ids":["known","missing","known"]}`)
	var result struct {
		Accounts map[string]auth.ExecutionCapacity `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.Accounts) != 1 || !result.Accounts["known"].Unlimited || result.Accounts["known"].ObservedAt.IsZero() || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid snapshot: %s", w.Body)
	}
	for _, body := range []string{`{}`, `{"ids":[]}`, `{"ids":[""]}`, `{"ids":[` + strings.Repeat(`"x",`, 200) + `"x"]}`, `{"ids":["` + strings.Repeat("x", 513) + `"]}`, `{"ids":["` + strings.Repeat("x", 128*1024) + `"]}`} {
		if run(body).Code != 400 {
			t.Fatal("unbounded input accepted")
		}
	}
}
