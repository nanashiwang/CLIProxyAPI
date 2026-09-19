package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type metadataRoundTripper func(*http.Request) (*http.Response, error)

func (f metadataRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type metadataUsageCapture struct {
	model   string
	records chan coreusage.Record
}

func (p *metadataUsageCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.Model == p.model {
		select {
		case p.records <- record:
		default:
		}
	}
}

func TestUsageClientEntryAndUpstreamTransportAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name, client, upstream             string
		stream, websocket, noEntry, failed bool
	}{
		{name: "http upstream SSE", client: "http", upstream: "sse"},
		{name: "SSE upstream HTTP", client: "sse", upstream: "http", stream: true},
		{name: "websocket upstream HTTP fallback", client: "ws", upstream: "http", stream: true, websocket: true},
		{name: "websocket upstream SSE fallback", client: "ws", upstream: "sse", stream: true, websocket: true},
		{name: "SSE fails with JSON", client: "sse", upstream: "http", stream: true, failed: true},
		{name: "SDK lacks client entry", client: "", upstream: "sse", stream: true, noEntry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := "usage-transport-" + strings.ReplaceAll(tc.name, " ", "-")
			capture := &metadataUsageCapture{model: model, records: make(chan coreusage.Record, 2)}
			coreusage.RegisterPlugin(capture)
			executor := &interceptorCaptureExecutor{}
			execute := func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
				reporter := helps.NewExecutorUsageReporter(ctx, executor, req.Model, auth)
				client := reporter.TrackHTTPClient(&http.Client{Transport: metadataRoundTripper(func(request *http.Request) (*http.Response, error) {
					contentType, body := "application/json", `{"model":"upstream"}`
					if tc.upstream == "sse" {
						contentType, body = "text/event-stream; charset=utf-8", "data: {\"model\":\"upstream\"}\n\n"
					}
					status := http.StatusOK
					if tc.failed {
						status = http.StatusBadRequest
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
				})})
				request, _ := http.NewRequestWithContext(ctx, "POST", "https://upstream.invalid/v1/responses", strings.NewReader(`{"model":"wire"}`))
				response, err := client.Do(request)
				if err != nil {
					return coreexecutor.Response{}, err
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if tc.failed {
					reporter.PublishFailure(ctx, errors.New("upstream rejected"))
					return coreexecutor.Response{}, &coreauth.Error{Code: "bad_request", Message: "upstream rejected", HTTPStatus: http.StatusBadRequest}
				}
				reporter.Publish(ctx, coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2})
				return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
			}
			executor.execute = execute
			executor.stream = func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
				response, err := execute(ctx, auth, req, opts)
				if err != nil {
					return nil, err
				}
				chunks := make(chan coreexecutor.StreamChunk, 1)
				chunks <- coreexecutor.StreamChunk{Payload: response.Payload}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}
			handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{})
			ctx := context.Background()
			if !tc.noEntry {
				ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginCtx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
				ginCtx.Request.RemoteAddr = "192.0.2.10:43210"
				ginCtx.Request.Header.Set("X-Forwarded-For", "203.0.113.200")
				ginCtx.Request.Header.Set("User-Agent", "entry-client/1")
				var cancel APIHandlerCancelFunc
				ctx, cancel = handler.GetContextWithCancel(nil, ginCtx, ctx)
				defer cancel()
			}
			if tc.websocket {
				ctx = coreexecutor.WithDownstreamWebsocket(ctx)
			}
			if tc.stream {
				data, _, errs := handler.ExecuteStreamWithAuthManager(ctx, "openai", model, []byte(`{"model":"`+model+`","stream":true}`), "")
				if data != nil {
					for range data {
					}
				}
				var gotError bool
				if errs != nil {
					for err := range errs {
						if err != nil {
							gotError = true
						}
					}
				}
				if gotError != tc.failed {
					t.Fatalf("stream error=%v want=%v", gotError, tc.failed)
				}
			} else if _, _, err := handler.ExecuteWithAuthManager(ctx, "openai", model, []byte(`{"model":"`+model+`"}`), ""); err != nil {
				t.Fatal(err)
			}
			select {
			case record := <-capture.records:
				if record.ClientTransport != tc.client || record.UpstreamTransport != tc.upstream || record.Failed != tc.failed {
					t.Fatalf("transport/failure=%s/%s/%v want=%s/%s/%v", record.ClientTransport, record.UpstreamTransport, record.Failed, tc.client, tc.upstream, tc.failed)
				}
				if tc.noEntry {
					if record.ClientIP != "" || record.UserAgent != "" {
						t.Fatal("SDK request invented client metadata")
					}
				} else if record.ClientIP != "192.0.2.10" || record.UserAgent != "entry-client/1" {
					t.Fatalf("unsafe or missing entry snapshot: %+v", record)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("usage record missing")
			}
		})
	}
}

func TestUsageClientSnapshotSurvivesGinContextReuse(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ginCtx.Request.RemoteAddr = "[2001:db8::10]:1234"
	ginCtx.Request.Header.Set("User-Agent", "old-client/1")
	ginCtx.Request.Header.Set("Upgrade", "websocket")
	ginCtx.Request.Header.Set("Connection", "Upgrade")
	handler := &BaseAPIHandler{Cfg: &sdkconfig.SDKConfig{}}
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()
	ctx = withUsageClientTransport(ctx, true)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ginCtx.Request.RemoteAddr = "198.51.100.22:5678"
	ginCtx.Request.Header.Set("User-Agent", "new-client/2")
	metadata := coreusage.ClientRequestMetadataFromContext(ctx)
	if metadata.Transport != "sse" || metadata.ClientIP != "2001:db8::10" || metadata.UserAgent != "old-client/1" {
		t.Fatalf("reused Gin context contaminated snapshot: %+v", metadata)
	}
	if got := coreusage.ClientRequestMetadataFromContext(withUsageClientTransport(coreexecutor.WithDownstreamWebsocket(ctx), true)).Transport; got != "ws" {
		t.Fatalf("established WS marker lost: %s", got)
	}
}

func TestUsageInternalModelCallsPreserveOuterClientTransport(t *testing.T) {
	for _, tc := range []struct {
		client      string
		innerStream bool
	}{
		{client: "http", innerStream: true}, {client: "sse", innerStream: false}, {client: "ws", innerStream: false},
	} {
		t.Run(tc.client, func(t *testing.T) {
			model := "internal-client-transport-" + tc.client
			captured := make(chan coreusage.ClientRequestMetadata, 1)
			executor := &interceptorCaptureExecutor{
				execute: func(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
					captured <- coreusage.ClientRequestMetadataFromContext(ctx)
					return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				stream: func(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
					captured <- coreusage.ClientRequestMetadataFromContext(ctx)
					chunks := make(chan coreexecutor.StreamChunk, 1)
					chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"ok":true}`)}
					close(chunks)
					return &coreexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{})
			ctx := coreusage.WithClientRequestMetadata(context.Background(), coreusage.ClientRequestMetadata{Transport: tc.client, ClientIP: "192.0.2.10", UserAgent: "outer-client/1"})
			req := ModelExecutionRequest{EntryProtocol: "openai", Model: model, Body: []byte(`{"model":"` + model + `"}`), Stream: tc.innerStream}
			if tc.innerStream {
				response, err := handler.ExecuteModelStream(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				for range response.Chunks {
				}
			} else if _, err := handler.ExecuteModel(ctx, req); err != nil {
				t.Fatal(err)
			}
			metadata := <-captured
			if metadata.Transport != tc.client || metadata.ClientIP != "192.0.2.10" || metadata.UserAgent != "outer-client/1" {
				t.Fatalf("internal mode overwrote outer client: %+v", metadata)
			}
		})
	}
}
