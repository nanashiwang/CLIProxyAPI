package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/poo"
)

func TestPendingStreamErrorReturnsBufferedError(t *testing.T) {
	errs := make(chan *interfaces.ErrorMessage, 1)
	want := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("upstream failed")}
	errs <- want
	close(errs)

	got, ok := PendingStreamError(errs)
	if !ok || got != want {
		t.Fatalf("PendingStreamError() = (%#v, %t), want (%#v, true)", got, ok, want)
	}
}

func TestValidateSSEDataJSONAllowsMultilinePayload(t *testing.T) {
	chunk := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"status\":\"completed\"}}\n\n")
	if err := validateSSEDataJSON(chunk); err != nil {
		t.Fatalf("validateSSEDataJSON() error = %v, want nil", err)
	}
}

func TestForwardStreamNormalizesErrorBeforeWriteAndCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("raw secret")}
	close(errs)

	var written, canceled string
	disabledKeepAlive := time.Duration(0)
	h := &BaseAPIHandler{}
	h.ForwardStream(c, recorder, func(err error) {
		if err != nil {
			canceled = err.Error()
		}
	}, data, errs, StreamForwardOptions{
		KeepAliveInterval: &disabledKeepAlive,
		NormalizeTerminalError: func(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
			return &interfaces.ErrorMessage{StatusCode: errMsg.StatusCode, Error: errors.New("safe error")}
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			written = errMsg.Error.Error()
		},
	})

	if written != "safe error" || canceled != "safe error" {
		t.Fatalf("written=%q canceled=%q, want sanitized error", written, canceled)
	}
}

func TestPendingStreamErrorIgnoresUnavailableErrors(t *testing.T) {
	closed := make(chan *interfaces.ErrorMessage)
	close(closed)

	for name, errs := range map[string]<-chan *interfaces.ErrorMessage{
		"nil":          nil,
		"closed empty": closed,
		"open empty":   make(chan *interfaces.ErrorMessage),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := PendingStreamError(errs); ok || got != nil {
				t.Fatalf("PendingStreamError() = (%#v, %t), want (nil, false)", got, ok)
			}
		})
	}
}

func TestForwardStreamWritesDoneBeforeTEEProof(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	data := make(chan []byte, 2)
	data <- []byte(`{"id":"chunk"}`)
	data <- poo.EncodeStreamEvent("proof", []byte(`{"pcr0":"abc"}`))
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	disabledKeepAlive := time.Duration(0)
	var canceled error
	h := &BaseAPIHandler{}
	h.ForwardStream(c, recorder, func(err error) { canceled = err }, data, errs, StreamForwardOptions{
		KeepAliveInterval: &disabledKeepAlive,
		WriteChunk: func(chunk []byte) {
			_, _ = recorder.WriteString("data: " + string(chunk) + "\n\n")
		},
		WriteDone: func() {
			_, _ = recorder.WriteString("data: [DONE]\n\n")
		},
	})

	want := "data: {\"id\":\"chunk\"}\n\ndata: [DONE]\n\nevent: tee.proof\ndata: {\"pcr0\":\"abc\"}\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if canceled != nil {
		t.Fatalf("cancel error = %v, want nil", canceled)
	}
}

func TestForwardStreamWritesTEEErrorWithoutDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	data := make(chan []byte, 2)
	data <- []byte(`{"id":"chunk"}`)
	data <- poo.EncodeStreamEvent("error", []byte(`{"error":{"type":"tee_error","message":"missing proof"}}`))
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	disabledKeepAlive := time.Duration(0)
	var canceled error
	h := &BaseAPIHandler{}
	h.ForwardStream(c, recorder, func(err error) { canceled = err }, data, errs, StreamForwardOptions{
		KeepAliveInterval: &disabledKeepAlive,
		WriteChunk: func(chunk []byte) {
			_, _ = recorder.WriteString("data: " + string(chunk) + "\n\n")
		},
		WriteDone: func() {
			_, _ = recorder.WriteString("data: [DONE]\n\n")
		},
	})

	got := recorder.Body.String()
	if strings.Contains(got, "[DONE]") {
		t.Fatalf("TEE error stream unexpectedly contains DONE: %q", got)
	}
	want := "data: {\"id\":\"chunk\"}\n\nevent: tee.error\ndata: {\"error\":{\"type\":\"tee_error\",\"message\":\"missing proof\"}}\n\n"
	if got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if canceled == nil {
		t.Fatal("cancel error = nil, want TEE failure")
	}
}
