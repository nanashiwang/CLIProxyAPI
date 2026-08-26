package poo

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func boolPtr(value bool) *bool { return &value }

func gatewayConfig(url string) config.PoOParentGatewayConfig {
	return config.PoOParentGatewayConfig{
		Enabled:  true,
		Required: boolPtr(true),
		URL:      url,
		AuthMode: "none",
		Timeout:  "2s",
	}
}

func writeGatewayResponse(t *testing.T, w http.ResponseWriter, status int, body, proof []byte) {
	t.Helper()
	w.Header().Set("Content-Type", relayContentType)
	head, err := json.Marshal(map[string]any{
		"status":  status,
		"headers": map[string]string{"Content-Type": "application/json", "X-Upstream": "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []struct {
		typ     byte
		payload []byte
	}{{frameRespHead, head}, {frameRespChunk, body}, {frameRespTrailer, proof}} {
		if err := writeFrame(w, frame.typ, frame.payload); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTransportRelaysFramesAndRecordsProof(t *testing.T) {
	requestBody := []byte(`{"model":"gpt-test","stream":false}`)
	proof := []byte(`{"pcr0":"abc","signature":"sig"}`)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-PoO-Proxy-URL"); got != "http://proxy.internal:8080" {
			t.Errorf("X-PoO-Proxy-URL = %q", got)
		}
		if got := r.Header.Get("X-PoO-Account-ID"); got != "account-1" {
			t.Errorf("X-PoO-Account-ID = %q", got)
		}
		typ, headPayload, err := readFrame(r.Body)
		if err != nil || typ != frameReqHead {
			t.Fatalf("request head frame = 0x%x, %v", typ, err)
		}
		var head struct {
			Nonce    string `json:"nonce"`
			Token    string `json:"token"`
			Upstream struct {
				Host    string            `json:"host"`
				Method  string            `json:"method"`
				Path    string            `json:"path"`
				Headers map[string]string `json:"headers"`
			} `json:"upstream"`
		}
		if err = json.Unmarshal(headPayload, &head); err != nil {
			t.Fatal(err)
		}
		if head.Nonce == "" || head.Token != "secret-token" || head.Upstream.Host != "api.openai.com" || head.Upstream.Method != http.MethodPost || head.Upstream.Path != "/v1/responses?x=1" {
			t.Errorf("unexpected request head: %+v", head)
		}
		if value, exists := head.Upstream.Headers["authorization"]; !exists || value != "" {
			t.Errorf("authorization sentinel missing: %#v", head.Upstream.Headers)
		}
		typ, gotBody, err := readFrame(r.Body)
		if err != nil || typ != frameReqBody || !bytes.Equal(gotBody, requestBody) {
			t.Fatalf("request body frame = 0x%x %q, %v", typ, gotBody, err)
		}
		writeGatewayResponse(t, w, http.StatusOK, []byte(`{"id":"resp_1"}`), proof)
	}))
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses?x=1", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	transport := NewTransport(gatewayConfig(gateway.URL), "http://proxy.internal:8080", "account-1", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	recordID := TakeRecordID(resp.Header)
	if recordID == "" || resp.Header.Get(InternalRecordHeader) != "" {
		t.Fatalf("record id handling failed: id=%q headers=%v", recordID, resp.Header)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), `{"id":"resp_1"}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	gotProof, err := AwaitResult(recordID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotProof, proof) {
		t.Fatalf("proof = %s, want %s", gotProof, proof)
	}
}

func TestTransportInjectsProofIntoUpstreamError(t *testing.T) {
	proof := []byte(`{"pcr0":"error-proof"}`)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, _ = readFrame(r.Body)
		_, _, _ = readFrame(r.Body)
		writeGatewayResponse(t, w, http.StatusUnauthorized, []byte(`{"error":{"message":"bad key"}}`), proof)
	}))
	defer gateway.Close()

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader([]byte(`{}`)))
	resp, err := NewTransport(gatewayConfig(gateway.URL), "", "", nil).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get(InternalRecordHeader) != "" {
		t.Fatalf("status=%d headers=%v", resp.StatusCode, resp.Header)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err = json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(value["proof"], proof) {
		t.Fatalf("proof = %s, want %s", value["proof"], proof)
	}
}

func TestTransportMissingTrailerIsRequestScoped(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, _ = readFrame(r.Body)
		_, _, _ = readFrame(r.Body)
		w.Header().Set("Content-Type", relayContentType)
		head, _ := json.Marshal(map[string]any{"status": http.StatusOK, "headers": map[string]string{}})
		_ = writeFrame(w, frameRespHead, head)
		_ = writeFrame(w, frameRespChunk, []byte(`{"partial":true}`))
	}))
	defer gateway.Close()

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader([]byte(`{}`)))
	resp, err := NewTransport(gatewayConfig(gateway.URL), "", "", nil).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	recordID := TakeRecordID(resp.Header)
	_, readErr := io.ReadAll(resp.Body)
	if readErr == nil || !IsError(readErr) {
		t.Fatalf("read error = %v, want PoO error", readErr)
	}
	var scoped interface{ IsRequestScoped() bool }
	if !errors.As(readErr, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("read error is not request-scoped: %v", readErr)
	}
	if _, err = AwaitResult(recordID, time.Second); err == nil || !IsError(err) {
		t.Fatalf("AwaitResult error = %v, want PoO error", err)
	}
}
