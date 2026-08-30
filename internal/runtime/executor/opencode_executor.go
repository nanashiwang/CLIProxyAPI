package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/opencode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/http/httpguts"
)

const (
	openCodeProvider             = "opencode"
	openCodeMaxBody              = 50 << 20
	maxOpenCodeSSEFrameBytes     = 16 << 20
	maxOpenCodeErrorMessageBytes = 8 << 10
)

// OpenCodeExecutor implements OpenCode Zen/Go's protocol-variable API.
type OpenCodeExecutor struct {
	cfg     *config.Config
	catalog *opencode.Catalog
}

// NewOpenCodeExecutor creates an OpenCode executor. The optional catalog keeps
// model capability and request protocol selection consistent across auths.
func NewOpenCodeExecutor(cfg *config.Config, catalogs ...*opencode.Catalog) *OpenCodeExecutor {
	catalog := (*opencode.Catalog)(nil)
	if len(catalogs) > 0 {
		catalog = catalogs[0]
	}
	if catalog == nil {
		catalog = opencode.NewCatalog()
	}
	return &OpenCodeExecutor{cfg: cfg, catalog: catalog}
}

func (e *OpenCodeExecutor) Identifier() string { return openCodeProvider }

// RequestToFormat reports the native protocol for the selected tier/model.
func (e *OpenCodeExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	protocol := e.protocolFor(nil, req.Model, opts)
	return protocolFormat(protocol)
}

func normalizeOpenCodeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func newOpenCodeHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	ctx = normalizeOpenCodeContext(ctx)
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

func (e *OpenCodeExecutor) PrepareRequestWithProtocol(req *http.Request, auth *cliproxyauth.Auth, protocol opencode.Protocol) error {
	if req == nil {
		return nil
	}
	validated, errValidate := validateOpenCodeRequest(req, auth)
	if errValidate != nil {
		return errValidate
	}
	if validated != protocol {
		return statusErr{code: http.StatusBadRequest, msg: "opencode executor: request protocol does not match the selected protocol"}
	}
	req.Host = ""
	sanitizeOpenCodeTransportHeaders(req.Header)
	applyOpenCodeHeaders(req, auth, protocol, nil, nil)
	return nil
}

func validateOpenCodeRequest(req *http.Request, auth *cliproxyauth.Auth) (opencode.Protocol, error) {
	if req == nil || req.URL == nil {
		return "", statusErr{code: http.StatusBadRequest, msg: "opencode executor: request URL is nil"}
	}
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), openCodeProvider) {
		return "", statusErr{code: http.StatusUnauthorized, msg: "opencode executor: OpenCode auth is required"}
	}
	tier := tierFromAuth(auth)
	base := ""
	if auth.Attributes != nil {
		base = strings.TrimSpace(auth.Attributes["base_url"])
		if rawTier := strings.ToLower(strings.TrimSpace(auth.Attributes["tier"])); rawTier != "" && rawTier != string(opencode.TierZen) && rawTier != string(opencode.TierGo) {
			return "", statusErr{code: http.StatusUnauthorized, msg: "opencode executor: invalid OpenCode tier"}
		}
	}
	if !config.IsAllowedOpenCodeBaseURLForTier(base, string(tier)) {
		return "", statusErr{code: http.StatusUnauthorized, msg: "opencode executor: invalid OpenCode base URL"}
	}
	u := req.URL
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "opencode.ai") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil || u.RawPath != "" {
		return "", statusErr{code: http.StatusBadRequest, msg: "opencode executor: request URL is not an allowed OpenCode endpoint"}
	}
	baseURL, errParse := url.Parse(base)
	if errParse != nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: "opencode executor: invalid OpenCode base URL"}
	}
	basePath := strings.TrimSuffix(baseURL.Path, "/")
	if !strings.HasPrefix(u.Path, basePath+"/") {
		return "", statusErr{code: http.StatusBadRequest, msg: "opencode executor: unsupported OpenCode endpoint"}
	}
	protocol := protocolForOpenCodePath(u.Path[len(basePath):])
	if protocol == "" {
		return "", statusErr{code: http.StatusBadRequest, msg: "opencode executor: unsupported OpenCode endpoint"}
	}
	if configured := protocolFromAuth(auth); configured != "" && configured != protocol {
		return "", statusErr{code: http.StatusBadRequest, msg: "opencode executor: request path does not match auth protocol"}
	}
	expectedPath := basePath + protocolOpenCodePath(protocol)
	if u.Path != expectedPath {
		return "", statusErr{code: http.StatusBadRequest, msg: "opencode executor: request URL does not match the selected OpenCode tier"}
	}
	if req.Method != http.MethodPost {
		return "", statusErr{code: http.StatusMethodNotAllowed, msg: "opencode executor: only POST is supported"}
	}
	return protocol, nil
}

func protocolOpenCodePath(protocol opencode.Protocol) string {
	switch protocol {
	case opencode.ProtocolResponses:
		return "/v1/responses"
	case opencode.ProtocolAnthropic:
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

func protocolForOpenCodePath(path string) opencode.Protocol {
	switch path {
	case "/v1/chat/completions":
		return opencode.ProtocolChat
	case "/v1/responses":
		return opencode.ProtocolResponses
	case "/v1/messages":
		return opencode.ProtocolAnthropic
	default:
		return ""
	}
}

func (e *OpenCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	protocol, errValidate := validateOpenCodeRequest(req, auth)
	if errValidate != nil {
		return errValidate
	}
	req.Host = ""
	sanitizeOpenCodeTransportHeaders(req.Header)
	applyOpenCodeHeaders(req, auth, protocol, nil, nil)
	return nil
}

func (e *OpenCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("opencode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	ctx = normalizeOpenCodeContext(ctx)
	httpReq := req.WithContext(ctx)
	protocol, errValidate := validateOpenCodeRequest(httpReq, auth)
	if errValidate != nil {
		return nil, errValidate
	}
	httpReq.Host = ""
	sanitizeOpenCodeTransportHeaders(httpReq.Header)
	if err := e.PrepareRequestWithProtocol(httpReq, auth, protocol); err != nil {
		return nil, err
	}
	return newOpenCodeHTTPClient(ctx, e.cfg, auth).Do(httpReq)
}

func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = normalizeOpenCodeContext(ctx)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	protocol := e.protocolFor(auth, baseModel, opts)
	to := protocolFormat(protocol)
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, originalTranslated, errTranslate := e.translate(ctx, auth, req, opts, to, false)
	if errTranslate != nil {
		return resp, errTranslate
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())
	endpoint, errEndpoint := e.endpoint(auth, protocol)
	if errEndpoint != nil {
		return resp, errEndpoint
	}
	httpReq, errRequest := e.newRequest(ctx, auth, protocol, endpoint, body, opts.Headers, opts.Metadata, false)
	if errRequest != nil {
		return resp, errRequest
	}
	e.recordRequest(ctx, httpReq, body, auth)
	httpClient := reporter.TrackHTTPClient(newOpenCodeHTTPClient(ctx, e.cfg, auth))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Debugf("opencode executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	bodyBytes, errRead := io.ReadAll(io.LimitReader(httpResp.Body, openCodeMaxBody+1))
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	if len(bodyBytes) > openCodeMaxBody {
		return resp, statusErr{code: http.StatusBadGateway, msg: "opencode response body is too large"}
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, newOpenCodeStatusErr(httpResp, bodyBytes)
	}
	if !json.Valid(bodyBytes) {
		return resp, statusErr{code: http.StatusBadGateway, msg: "opencode response body is invalid JSON"}
	}
	reporter.Publish(ctx, parseOpenCodeUsage(protocol, bodyBytes))
	reporter.EnsurePublished(ctx)
	if streamErr, ok := openCodeResponseBodyError(protocol, bodyBytes); ok {
		return resp, streamErr
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := helps.TranslateOpenCodeNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, originalTranslated, bodyBytes, &param)
	if cliproxyexecutor.ResponseFormatOrSource(opts) == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = normalizeOpenCodeContext(ctx)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	protocol := e.protocolFor(auth, baseModel, opts)
	to := protocolFormat(protocol)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, originalTranslated, errTranslate := e.translate(ctx, auth, req, opts, to, true)
	if errTranslate != nil {
		return nil, errTranslate
	}
	if protocol == opencode.ProtocolChat {
		body = helps.SetBoolIfDifferent(body, "stream_options.include_usage", true)
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())
	endpoint, errEndpoint := e.endpoint(auth, protocol)
	if errEndpoint != nil {
		return nil, errEndpoint
	}
	httpReq, errRequest := e.newRequest(ctx, auth, protocol, endpoint, body, opts.Headers, opts.Metadata, true)
	if errRequest != nil {
		return nil, errRequest
	}
	e.recordRequest(ctx, httpReq, body, auth)
	httpClient := reporter.TrackHTTPClient(newOpenCodeHTTPClient(ctx, e.cfg, auth))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		defer httpResp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(httpResp.Body, openCodeMaxBody+1))
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		return nil, newOpenCodeStatusErr(httpResp, data)
	}

	out := make(chan cliproxyexecutor.StreamChunk, 8)
	go e.stream(ctx, httpResp, req, opts, protocol, to, responseFormat, originalTranslated, reporter, out)
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenCodeExecutor) stream(ctx context.Context, resp *http.Response, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, protocol opencode.Protocol, to, responseFormat sdktranslator.Format, translated []byte, reporter *helps.UsageReporter, out chan<- cliproxyexecutor.StreamChunk) {
	defer close(out)
	defer resp.Body.Close()
	usageSeen := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), maxOpenCodeSSEFrameBytes+4096)
	var dataLines [][]byte
	frameBytes := 0
	var eventName string
	var param any
	state := helps.NewClaudeInputTokenState(opts.SourceFormat, to, responseFormat, opts.OriginalRequest)
	failed := false
	seenTerminal := false

	emitError := func(err error) {
		if err == nil {
			return
		}
		err = limitOpenCodeError(err)
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		reporter.PublishFailure(ctx, err)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: err}:
		case <-ctx.Done():
		}
		failed = true
	}
	processFrame := func() bool {
		if len(dataLines) == 0 {
			dataLines = nil
			eventName = ""
			frameBytes = 0
			return false
		}
		data := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
		dataLines = nil
		event := eventName
		eventName = ""
		frameBytes = 0
		if bytes.Equal(data, []byte("[DONE]")) {
			if seenTerminal {
				// A Responses terminal event already produced the downstream finish.
				return true
			}
			seenTerminal = true
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param, state)
			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return true
				}
			}
			return true
		}
		// Once a terminal event is observed, tolerate a trailing [DONE] but
		// discard every other frame so an upstream cannot append content after
		// completion or inject a second error.
		if seenTerminal {
			return false
		}
		if !json.Valid(data) {
			emitError(statusErr{code: http.StatusBadGateway, msg: "opencode stream contained invalid SSE JSON"})
			return true
		}
		if event != "" && gjson.GetBytes(data, "type").String() == "" {
			data = helps.SetStringIfDifferent(data, "type", event)
		}
		if streamError, ok := openAICompatStreamDataError(data, event); ok {
			emitError(streamError)
			return true
		}
		if protocol == opencode.ProtocolResponses {
			responseStatus := strings.ToLower(strings.TrimSpace(gjson.GetBytes(data, "response.status").String()))
			if responseStatus == "failed" || responseStatus == "error" {
				emitError(statusErr{code: http.StatusBadGateway, msg: "opencode Responses request failed"})
				return true
			}
		}
		if detail, ok := parseOpenCodeStreamUsage(protocol, data); ok {
			reporter.Publish(ctx, detail)
			usageSeen = true
		}
		typeName := gjson.GetBytes(data, "type").String()
		if protocol == opencode.ProtocolResponses && (typeName == "response.completed" || typeName == "response.incomplete" || typeName == "response.done") {
			seenTerminal = true
			if typeName == "response.done" {
				status := strings.ToLower(strings.TrimSpace(gjson.GetBytes(data, "response.status").String()))
				canonicalType := "response.completed"
				if status == "incomplete" {
					canonicalType = "response.incomplete"
				}
				// Codex response translators consume response.completed or
				// response.incomplete; normalize legacy response.done events.
				data = helps.SetStringIfDifferent(data, "type", canonicalType)
			}
		}
		line := append([]byte("data: "), data...)
		chunks := helps.TranslateOpenCodeStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, line, &param)
		for _, chunk := range chunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return true
			}
		}
		return false
	}

	for scanner.Scan() {
		rawLine := scanner.Bytes()
		line := bytes.TrimSpace(rawLine)
		if len(line) > 0 {
			frameBytes += len(rawLine) + 1
			if frameBytes > maxOpenCodeSSEFrameBytes {
				emitError(statusErr{code: http.StatusBadGateway, msg: "opencode stream SSE frame is too large"})
				break
			}
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		if len(line) == 0 {
			if processFrame() {
				break
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			dataLines = append(dataLines, bytes.Clone(bytes.TrimSpace(line[len("data:"):])))
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			eventName = safeSSEValue(line[len("event:"):])
			continue
		}
		if bytes.HasPrefix(line, []byte(":")) || bytes.HasPrefix(line, []byte("id:")) || bytes.HasPrefix(line, []byte("retry:")) {
			continue
		}
		if json.Valid(line) {
			dataLines = append(dataLines, bytes.Clone(line))
			continue
		}
		emitError(statusErr{code: http.StatusBadGateway, msg: "opencode stream contained an invalid line"})
		break
	}
	if !failed && scanner.Err() != nil {
		scanErr := scanner.Err()
		if strings.Contains(scanErr.Error(), "token too long") {
			scanErr = statusErr{code: http.StatusBadGateway, msg: "opencode stream SSE frame is too large"}
		}
		emitError(scanErr)
	}
	if !failed && !seenTerminal && len(dataLines) > 0 {
		processFrame()
	}
	if !failed && scanner.Err() == nil && !seenTerminal && protocol == opencode.ProtocolResponses {
		emitError(statusErr{code: http.StatusBadGateway, msg: "opencode response stream closed before terminal event"})
	}
	if !failed && !usageSeen {
		reporter.EnsurePublished(ctx)
	}
}

func (e *OpenCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = normalizeOpenCodeContext(ctx)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	to := sdktranslator.FormatOpenAI
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, opts.SourceFormat, to, baseModel, req.Payload, false, false)
	translated, err := helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("opencode executor: tokenizer init failed: %w", err)
	}
	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("opencode executor: token counting failed: %w", err)
	}
	return cliproxyexecutor.Response{Payload: sdktranslator.TranslateTokenCount(ctx, to, cliproxyexecutor.ResponseFormatOrSource(opts), count, helps.BuildOpenAIUsageJSON(count))}, nil
}

func (e *OpenCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	ctx = normalizeOpenCodeContext(ctx)
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func (e *OpenCodeExecutor) translate(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, to sdktranslator.Format, stream bool) ([]byte, []byte, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	var originalTranslated, translated []byte
	if to == sdktranslator.FormatCodex {
		originalTranslated = helps.TranslateOpenCodeRequestWithContext(ctx, opts.SourceFormat, baseModel, original, stream)
		translated = helps.TranslateOpenCodeRequestWithContext(ctx, opts.SourceFormat, baseModel, req.Payload, stream)
	} else {
		originalTranslated = helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, opts.SourceFormat, to, baseModel, original, stream, false)
		translated = helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, opts.SourceFormat, to, baseModel, req.Payload, stream, false)
	}
	var err error
	translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, nil, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), opts.SourceFormat.String(), "", translated, originalTranslated, requestedModel, helps.PayloadRequestPath(opts), opts.Headers)
	translated = helps.SetStringIfDifferent(translated, "model", baseModel)
	return translated, originalTranslated, nil
}

func (e *OpenCodeExecutor) protocolFor(auth *cliproxyauth.Auth, model string, opts cliproxyexecutor.Options) opencode.Protocol {
	model = strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	tier := tierFromAuthAndOptions(auth, opts)
	if e.cfg != nil {
		if protocol := configuredProtocol(e.cfg.OpenCode.ProtocolOverrides, tier, model); protocol != "" {
			return protocol
		}
	}
	if e.catalog != nil {
		if protocol := e.catalog.Protocol(tier, model); protocol != "" {
			return protocol
		}
	}
	if protocol := protocolFromAuth(auth); protocol != "" {
		return protocol
	}
	if opts.SourceFormat == sdktranslator.FormatClaude {
		return opencode.ProtocolAnthropic
	}
	return opencode.ProtocolChat
}

func (e *OpenCodeExecutor) endpoint(auth *cliproxyauth.Auth, protocol opencode.Protocol) (string, error) {
	base := ""
	if auth != nil && auth.Attributes != nil {
		base = strings.TrimSpace(auth.Attributes["base_url"])
	}
	tier := tierFromAuth(auth)
	if auth != nil && auth.Attributes != nil {
		if rawTier := strings.ToLower(strings.TrimSpace(auth.Attributes["tier"])); rawTier != "" && rawTier != string(opencode.TierZen) && rawTier != string(opencode.TierGo) {
			return "", statusErr{code: http.StatusUnauthorized, msg: "invalid OpenCode tier"}
		}
	}
	if !config.IsAllowedOpenCodeBaseURLForTier(base, string(tier)) {
		return "", statusErr{code: http.StatusUnauthorized, msg: "invalid OpenCode base URL"}
	}
	path := "/v1/chat/completions"
	switch protocol {
	case opencode.ProtocolResponses:
		path = "/v1/responses"
	case opencode.ProtocolAnthropic:
		path = "/v1/messages"
	}
	return strings.TrimSuffix(base, "/") + path, nil
}

func (e *OpenCodeExecutor) newRequest(ctx context.Context, auth *cliproxyauth.Auth, protocol opencode.Protocol, endpoint string, body []byte, inbound http.Header, metadata map[string]any, stream bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	for _, name := range []string{"x-opencode-session", "x-session-affinity", "X-Session-Id", "x-session-id", "conversation-id", "x-opencode-project", "x-parent-session-id"} {
		if value := safeHeaderValue(inbound.Get(name)); value != "" {
			req.Header.Set(name, value)
		}
	}
	applyOpenCodeHeaders(req, auth, protocol, inbound, metadata)
	return req, nil
}

func (e *OpenCodeExecutor) recordRequest(ctx context.Context, req *http.Request, body []byte, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID, authLabel = auth.ID, auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: req.URL.String(), Method: req.Method, Headers: req.Header.Clone(), Body: body, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})
}

func applyOpenCodeHeaders(req *http.Request, auth *cliproxyauth.Auth, protocol opencode.Protocol, inbound http.Header, metadata map[string]any) {
	if req == nil {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	key := ""
	if auth != nil && auth.Attributes != nil {
		key = strings.TrimSpace(auth.Attributes["api_key"])
	}
	req.Header.Set("User-Agent", fmt.Sprintf("opencode/1.18.21 (%s %s; %s)", runtime.GOOS, runtime.GOARCH, runtime.Version()))
	req.Header.Set("x-opencode-client", "cli")
	session := ""
	if inbound != nil {
		session = firstSafeHeader(inbound, "x-opencode-session", "x-session-affinity", "X-Session-Id", "x-session-id", "conversation-id")
	}
	if session == "" {
		session = helps.ProviderSessionUUID(openCodeProvider, metadata)
	}
	if session == "" {
		session = uuid.NewString()
	}
	req.Header.Set("x-opencode-session", session)
	req.Header.Set("x-session-affinity", session)
	req.Header.Set("X-Session-Id", session)
	req.Header.Set("x-opencode-request", uuid.NewString())
	project := "opencode:default-project"
	if inbound != nil {
		if value := safeHeaderValue(inbound.Get("x-opencode-project")); value != "" {
			project = value
		}
	}
	req.Header.Set("x-opencode-project", project)
	if inbound != nil {
		if value := safeHeaderValue(inbound.Get("x-parent-session-id")); value != "" {
			req.Header.Set("x-parent-session-id", value)
		}
	}
	applySafeOpenCodeCustomHeaders(req, auth)
	// Built-in authentication and session headers always win over user config.
	if protocol == opencode.ProtocolAnthropic {
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
	} else {
		req.Header.Del("x-api-key")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		} else {
			req.Header.Del("Authorization")
		}
	}
	req.Header.Set("x-opencode-session", session)
	req.Header.Set("x-session-affinity", session)
	req.Header.Set("X-Session-Id", session)
	req.Header.Set("x-opencode-request", req.Header.Get("x-opencode-request"))
}

func sanitizeOpenCodeTransportHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	for _, name := range []string{
		"Host", "Connection", "Proxy-Connection", "Proxy-Authorization",
		"Transfer-Encoding", "TE", "Trailer", "Upgrade",
	} {
		headers.Del(name)
	}
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "proxy-") {
			headers.Del(name)
		}
	}
}

func applySafeOpenCodeCustomHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil || auth == nil {
		return
	}
	protected := map[string]struct{}{
		"authorization": {}, "x-api-key": {}, "host": {}, "content-length": {},
		"content-type": {}, "accept": {}, "connection": {}, "transfer-encoding": {},
		"x-opencode-session": {}, "x-session-affinity": {}, "x-session-id": {},
		"x-opencode-request": {}, "x-opencode-project": {}, "x-parent-session-id": {},
		"x-opencode-client": {},
	}
	keys := make([]string, 0, len(auth.Attributes))
	for key := range auth.Attributes {
		if strings.HasPrefix(strings.ToLower(key), "header:") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		name := strings.TrimSpace(key[len("header:"):])
		if !httpguts.ValidHeaderFieldName(name) {
			continue
		}
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		value := safeHeaderValue(auth.Attributes[key])
		if canonical == "" || value == "" {
			continue
		}
		lowerCanonical := strings.ToLower(canonical)
		if _, blocked := protected[lowerCanonical]; blocked || strings.HasPrefix(lowerCanonical, "proxy-") {
			continue
		}
		if _, exists := seen[lowerCanonical]; exists {
			continue
		}
		seen[lowerCanonical] = struct{}{}
		req.Header.Set(canonical, value)
	}
}

func protocolFromAuth(auth *cliproxyauth.Auth) opencode.Protocol {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(auth.Attributes["protocol"])) {
	case "chat":
		return opencode.ProtocolChat
	case "responses":
		return opencode.ProtocolResponses
	case "anthropic":
		return opencode.ProtocolAnthropic
	}
	return ""
}
func tierFromAuthAndOptions(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) opencode.Tier {
	if auth != nil {
		return tierFromAuth(auth)
	}
	if opts.Metadata != nil && strings.EqualFold(strings.TrimSpace(metadataString(opts.Metadata, cliproxyexecutor.SelectedAuthTierMetadataKey)), string(opencode.TierGo)) {
		return opencode.TierGo
	}
	return opencode.TierZen
}

func tierFromAuth(auth *cliproxyauth.Auth) opencode.Tier {
	if auth != nil && auth.Attributes != nil && strings.EqualFold(strings.TrimSpace(auth.Attributes["tier"]), "go") {
		return opencode.TierGo
	}
	return opencode.TierZen
}
func configuredProtocol(overrides map[string]string, tier opencode.Tier, model string) opencode.Protocol {
	keys := []string{string(tier) + "/" + model, model}
	for _, key := range keys {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if value, exists := overrides[normalizedKey]; exists {
			if protocol, ok := parseConfiguredProtocol(value); ok {
				return protocol
			}
		}
	}
	// Tolerate programmatically supplied maps that have not passed config
	// sanitization, while keeping case-insensitive duplicates deterministic.
	rawKeys := make([]string, 0, len(overrides))
	for raw := range overrides {
		rawKeys = append(rawKeys, raw)
	}
	sort.Strings(rawKeys)
	for _, key := range keys {
		for _, raw := range rawKeys {
			if !strings.EqualFold(strings.TrimSpace(raw), key) {
				continue
			}
			if protocol, ok := parseConfiguredProtocol(overrides[raw]); ok {
				return protocol
			}
		}
	}
	return ""
}

func parseConfiguredProtocol(value string) (opencode.Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "chat":
		return opencode.ProtocolChat, true
	case "responses":
		return opencode.ProtocolResponses, true
	case "anthropic":
		return opencode.ProtocolAnthropic, true
	default:
		return "", false
	}
}

func protocolFormat(protocol opencode.Protocol) sdktranslator.Format {
	switch protocol {
	case opencode.ProtocolResponses:
		return sdktranslator.FormatCodex
	case opencode.ProtocolAnthropic:
		return sdktranslator.FormatClaude
	default:
		return sdktranslator.FormatOpenAI
	}
}
func newOpenCodeStatusErr(resp *http.Response, body []byte) statusErr {
	err := statusErr{code: resp.StatusCode, msg: truncateOpenCodeErrorMessage(helps.SummarizeErrorBody(resp.Header.Get("Content-Type"), body))}
	if resp.StatusCode == http.StatusTooManyRequests {
		err.retryAfter = parseOpenCodeRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	return err
}

func limitOpenCodeError(err error) error {
	switch value := err.(type) {
	case statusErr:
		return limitOpenCodeStatusErr(value)
	case *statusErr:
		if value == nil {
			return err
		}
		limited := limitOpenCodeStatusErr(*value)
		return &limited
	default:
		return err
	}
}

func limitOpenCodeStatusErr(err statusErr) statusErr {
	err.msg = truncateOpenCodeErrorMessage(err.msg)
	return err
}

func truncateOpenCodeErrorMessage(message string) string {
	if len(message) <= maxOpenCodeErrorMessageBytes {
		return message
	}
	limit := maxOpenCodeErrorMessageBytes - len("…")
	message = message[:limit]
	for len(message) > 0 && !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message + "…"
}

func parseOpenCodeRetryAfter(raw string, now time.Time) *time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var d time.Duration
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		if seconds > int64((7*24*time.Hour)/time.Second) {
			seconds = int64((7 * 24 * time.Hour) / time.Second)
		}
		d = time.Duration(seconds) * time.Second
	} else if deadline, err := http.ParseTime(raw); err == nil {
		d = deadline.Sub(now)
		if d <= 0 {
			return nil
		}
		if d > 7*24*time.Hour {
			d = 7 * 24 * time.Hour
		}
	} else {
		return nil
	}
	return &d
}

func parseOpenCodeUsage(protocol opencode.Protocol, body []byte) usage.Detail {
	if protocol == opencode.ProtocolAnthropic {
		return helps.ParseClaudeUsage(body)
	}
	return helps.ParseOpenAIUsage(body)
}
func parseOpenCodeStreamUsage(protocol opencode.Protocol, data []byte) (usage.Detail, bool) {
	if protocol == opencode.ProtocolAnthropic {
		return helps.ParseClaudeStreamUsage(data)
	}
	return helps.ParseOpenAIStreamUsage(data)
}
func safeSSEValue(value []byte) string {
	value = bytes.TrimSpace(value)
	if bytes.IndexByte(value, '\r') >= 0 || bytes.IndexByte(value, '\n') >= 0 {
		return ""
	}
	if len(value) > 256 {
		return string(value[:256])
	}
	return string(value)
}
func safeHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") || len(value) > 512 {
		return ""
	}
	return value
}
func firstSafeHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := safeHeaderValue(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func openCodeResponseBodyError(protocol opencode.Protocol, body []byte) (statusErr, bool) {
	if !json.Valid(body) {
		return statusErr{}, false
	}
	root := gjson.ParseBytes(body)
	typeName := strings.ToLower(strings.TrimSpace(root.Get("type").String()))
	if err, ok := openAICompatStreamDataError(body, typeName); ok {
		return limitOpenCodeStatusErr(err), true
	}
	if protocol != opencode.ProtocolResponses {
		return statusErr{}, false
	}
	status := strings.ToLower(strings.TrimSpace(root.Get("status").String()))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(root.Get("response.status").String()))
	}
	if typeName != "response.failed" && status != "failed" && status != "error" {
		return statusErr{}, false
	}
	return statusErr{code: http.StatusBadGateway, msg: "opencode Responses request failed"}, true
}
