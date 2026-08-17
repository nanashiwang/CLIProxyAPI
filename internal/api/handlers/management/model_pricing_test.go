package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCustomModelPricingCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("pricing:\n  enabled: true\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	handler := NewHandler(cfg, configPath, nil)
	reloads := make(chan *config.Config, 2)
	handler.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		reloads <- cfg
	})

	router := gin.New()
	router.PUT("/model-pricing/custom/*model", handler.PutCustomModelPricing)
	router.GET("/model-pricing/custom/*model", handler.GetCustomModelPricing)
	router.DELETE("/model-pricing/custom/*model", handler.DeleteCustomModelPricing)

	putBody := []byte(`{"provider":"openai","input":1.25,"output":4.5,"cache-read":0.125,"cache-write":1.5625}`)
	putRecorder := httptest.NewRecorder()
	router.ServeHTTP(putRecorder, httptest.NewRequest(http.MethodPut, "/model-pricing/custom/gpt-custom", bytes.NewReader(putBody)))
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", putRecorder.Code, putRecorder.Body.String())
	}
	select {
	case snapshot := <-reloads:
		if _, exists := snapshot.Pricing.Overrides["gpt-custom"]; !exists {
			t.Fatalf("reload snapshot = %+v", snapshot.Pricing)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pricing reload")
	}

	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/model-pricing/custom/gpt-custom", nil))
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", getRecorder.Code, getRecorder.Body.String())
	}
	var getBody struct {
		Model    string                 `json:"model"`
		Override config.PricingOverride `json:"override"`
	}
	if errDecode := json.Unmarshal(getRecorder.Body.Bytes(), &getBody); errDecode != nil {
		t.Fatalf("json.Unmarshal() error = %v", errDecode)
	}
	if getBody.Model != "gpt-custom" || getBody.Override.Input == nil || *getBody.Override.Input != 1.25 {
		t.Fatalf("GET body = %+v", getBody)
	}

	persisted, errReload := config.LoadConfig(configPath)
	if errReload != nil {
		t.Fatalf("LoadConfig() persisted error = %v", errReload)
	}
	if _, exists := persisted.Pricing.Overrides["gpt-custom"]; !exists {
		t.Fatalf("persisted pricing = %+v", persisted.Pricing)
	}

	deleteRecorder := httptest.NewRecorder()
	router.ServeHTTP(deleteRecorder, httptest.NewRequest(http.MethodDelete, "/model-pricing/custom/gpt-custom", nil))
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	select {
	case snapshot := <-reloads:
		if _, exists := snapshot.Pricing.Overrides["gpt-custom"]; exists {
			t.Fatalf("delete reload snapshot = %+v", snapshot.Pricing)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete reload")
	}
}

func TestPutCustomModelPricingValidatesPrices(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&config.Config{Pricing: config.DefaultPricingConfig()}, filepath.Join(t.TempDir(), "config.yaml"), nil)
	router := gin.New()
	router.PUT("/model-pricing/custom/*model", handler.PutCustomModelPricing)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/model-pricing/custom/gpt-custom", bytes.NewBufferString(`{"provider":"openai"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetModelPricingRejectsInvalidLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(&config.Config{}, "", nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/model-pricing?limit=invalid", nil)

	handler.GetModelPricing(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
