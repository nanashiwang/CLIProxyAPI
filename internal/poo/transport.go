package poo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const relayContentType = "application/vnd.poo.frames"

var hopByHopHeaders = map[string]struct{}{
	"connection": {}, "keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {},
	"proxy-connection": {}, "te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
}

type Transport struct {
	cfg       config.PoOParentGatewayConfig
	proxyURL  string
	accountID string
	fallback  http.RoundTripper

	once       sync.Once
	gatewayRT  http.RoundTripper
	gatewayErr error
}

func NewTransport(cfg config.PoOParentGatewayConfig, proxyURL, accountID string, fallback http.RoundTripper) *Transport {
	if fallback == nil {
		fallback = http.DefaultTransport
	}
	return &Transport{cfg: cfg, proxyURL: strings.TrimSpace(proxyURL), accountID: strings.TrimSpace(accountID), fallback: fallback}
}

func (t *Transport) IsPoOTransport() bool { return t != nil && t.cfg.Enabled }

func IsPoOTransport(rt http.RoundTripper) bool {
	type marker interface{ IsPoOTransport() bool }
	value, ok := rt.(marker)
	return ok && value.IsPoOTransport()
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || !t.cfg.Enabled {
		return t.fallback.RoundTrip(req)
	}
	if req == nil || req.URL == nil {
		return nil, &Error{Message: "PoO request is nil"}
	}
	if isControlPlaneRequest(req.URL) {
		return t.fallback.RoundTrip(req)
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return t.preflightFallback(req, nil, fmt.Errorf("PoO only supports HTTPS upstreams"))
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return t.preflightFallback(req, nil, fmt.Errorf("PoO only supports upstream port 443"))
	}

	body, err := readRequestBody(req, t.cfg.BodyLimit())
	if err != nil {
		return t.preflightFallback(req, nil, err)
	}
	restoreRequestBody(req, body)

	headPayload, requestID, err := buildRequestHead(req, body)
	if err != nil {
		return t.preflightFallback(req, body, err)
	}
	var relayBody bytes.Buffer
	if err = writeFrame(&relayBody, frameReqHead, headPayload); err == nil {
		err = writeFrame(&relayBody, frameReqBody, body)
	}
	if err != nil {
		return t.preflightFallback(req, body, err)
	}

	gatewayURL, err := url.Parse(t.cfg.RelayURL())
	if err != nil {
		return t.preflightFallback(req, body, fmt.Errorf("invalid PoO gateway URL: %w", err))
	}
	if err = validateGatewayURL(gatewayURL, t.cfg.AuthMode); err != nil {
		return t.preflightFallback(req, body, err)
	}

	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, t.cfg.RequestTimeout())
	relayReq, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL.String(), bytes.NewReader(relayBody.Bytes()))
	if err != nil {
		cancel()
		return t.preflightFallback(req, body, err)
	}
	relayReq.Header.Set("Content-Type", relayContentType)
	relayReq.Header.Set("X-PoO-Request-ID", requestID)
	if t.proxyURL != "" && !strings.EqualFold(t.proxyURL, "direct") {
		relayReq.Header.Set("X-PoO-Proxy-URL", t.proxyURL)
	}
	if t.accountID != "" {
		relayReq.Header.Set("X-PoO-Account-ID", t.accountID)
	}

	gatewayRT, err := t.gatewayTransport()
	if err != nil {
		cancel()
		return t.preflightFallback(req, body, err)
	}
	relayResp, err := gatewayRT.RoundTrip(relayReq)
	if err != nil {
		cancel()
		return nil, &Error{Message: "PoO gateway request failed", Cause: err, Submitted: true}
	}
	if relayResp.StatusCode < 200 || relayResp.StatusCode >= 300 {
		problem, _ := io.ReadAll(io.LimitReader(relayResp.Body, 1024*1024))
		_ = relayResp.Body.Close()
		cancel()
		return nil, &Error{Message: fmt.Sprintf("PoO gateway returned HTTP %d: %s", relayResp.StatusCode, strings.TrimSpace(string(problem))), Submitted: true}
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(relayResp.Header.Get("Content-Type"), ";")[0])); mediaType != relayContentType {
		_ = relayResp.Body.Close()
		cancel()
		return nil, &Error{Message: "PoO gateway returned an unexpected content type", Submitted: true}
	}

	typ, payload, err := readFrame(relayResp.Body)
	if err != nil {
		_ = relayResp.Body.Close()
		cancel()
		return nil, &Error{Message: "read PoO response head", Cause: err, Submitted: true}
	}
	if typ == frameError {
		_ = relayResp.Body.Close()
		cancel()
		return nil, &Error{Message: "PoO enclave rejected request: " + safeFrameMessage(payload), Submitted: true}
	}
	if typ != frameRespHead {
		_ = relayResp.Body.Close()
		cancel()
		return nil, &Error{Message: fmt.Sprintf("expected RESP_HEAD, got 0x%02x", typ), Submitted: true}
	}
	status, headers, err := parseResponseHead(payload)
	if err != nil {
		_ = relayResp.Body.Close()
		cancel()
		return nil, &Error{Message: "parse PoO response head", Cause: err, Submitted: true}
	}

	recordID, err := randomBase64(18)
	if err != nil {
		_ = relayResp.Body.Close()
		cancel()
		return nil, &Error{Message: "create PoO response recorder", Cause: err, Submitted: true}
	}
	recorder := newRecord(recordID)
	bodyReader := &frameBody{source: relayResp.Body, record: recorder, cancel: cancel}
	headers.Set(InternalRecordHeader, recordID)
	headers.Del("Connection")
	headers.Del("Transfer-Encoding")

	response := &http.Response{
		StatusCode: status,
		Status:     strconv.Itoa(status) + " " + http.StatusText(status),
		Header:     headers,
		Body:       bodyReader,
		Request:    req,
	}
	if status >= 200 && status < 300 {
		return response, nil
	}

	// Buffer upstream errors so their final JSON envelope can retain the TEE proof.
	errorBody, readErr := io.ReadAll(bodyReader)
	_ = bodyReader.Close()
	if readErr != nil {
		return nil, readErr
	}
	proof, proofErr := AwaitResult(recordID, 0)
	if proofErr != nil {
		return nil, &Error{Message: "PoO proof missing from upstream error", Cause: proofErr, Submitted: true}
	}
	response.Header.Del(InternalRecordHeader)
	injectedErrorBody, injectErr := InjectProof(errorBody, proof)
	if injectErr != nil {
		injectedErrorBody = errorBody
	}
	response.Body = io.NopCloser(bytes.NewReader(injectedErrorBody))
	response.ContentLength = int64(len(injectedErrorBody))
	response.Header.Del("Content-Length")
	return response, nil
}

func (t *Transport) preflightFallback(req *http.Request, body []byte, err error) (*http.Response, error) {
	if !t.cfg.IsRequired() {
		if body != nil {
			restoreRequestBody(req, body)
		}
		return t.fallback.RoundTrip(req)
	}
	return nil, &Error{Message: "PoO preflight failed", Cause: err, Submitted: false}
}

func (t *Transport) gatewayTransport() (http.RoundTripper, error) {
	t.once.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		mode := strings.ToLower(strings.TrimSpace(t.cfg.AuthMode))
		if mode == "" || mode == "none" {
			t.gatewayRT = transport
			return
		}
		if mode != "mtls" {
			t.gatewayErr = fmt.Errorf("unsupported PoO gateway auth mode %q", t.cfg.AuthMode)
			return
		}
		certificate, err := tls.LoadX509KeyPair(t.cfg.CertFile, t.cfg.KeyFile)
		if err != nil {
			t.gatewayErr = fmt.Errorf("load PoO client certificate: %w", err)
			return
		}
		caPEM, err := os.ReadFile(t.cfg.CAFile)
		if err != nil {
			t.gatewayErr = fmt.Errorf("read PoO CA file: %w", err)
			return
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			t.gatewayErr = errors.New("PoO CA file contains no certificates")
			return
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: strings.TrimSpace(t.cfg.ServerName)}
		t.gatewayRT = transport
	})
	return t.gatewayRT, t.gatewayErr
}

func validateGatewayURL(value *url.URL, authMode string) error {
	if value == nil || value.Host == "" {
		return errors.New("PoO gateway URL is missing a host")
	}
	mode := strings.ToLower(strings.TrimSpace(authMode))
	if mode == "" || mode == "none" {
		if value.Scheme != "http" || !isLoopbackHost(value.Hostname()) {
			return errors.New("PoO auth-mode none requires an HTTP loopback gateway URL")
		}
		return nil
	}
	if mode == "mtls" && value.Scheme != "https" {
		return errors.New("PoO auth-mode mtls requires an HTTPS gateway URL")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func readRequestBody(req *http.Request, limit int64) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	reader := io.LimitReader(req.Body, limit+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read upstream request body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("upstream request body exceeds PoO limit %d", limit)
	}
	return body, nil
}

func restoreRequestBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
}

func buildRequestHead(req *http.Request, body []byte) ([]byte, string, error) {
	nonce, err := randomBase64(32)
	if err != nil {
		return nil, "", err
	}
	requestID, err := randomBase64(18)
	if err != nil {
		return nil, "", err
	}

	headers := make(map[string]string)
	ordered := make([][2]string, 0, len(req.Header)+3)
	host := req.URL.Hostname()
	ordered = append(ordered, [2]string{"Host", host})
	keys := make([]string, 0, len(req.Header))
	for key := range req.Header {
		if _, skip := hopByHopHeaders[strings.ToLower(key)]; !skip {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	token := ""
	for _, key := range keys {
		values := req.Header.Values(key)
		value := strings.Join(values, ", ")
		lower := strings.ToLower(key)
		if lower == "authorization" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") {
			token = strings.TrimSpace(value[len("Bearer "):])
			value = ""
		}
		headers[lower] = value
		ordered = append(ordered, [2]string{http.CanonicalHeaderKey(key), value})
	}
	if req.Body != nil {
		headers["content-length"] = strconv.Itoa(len(body))
		ordered = append(ordered, [2]string{"Content-Length", ""})
	}

	head := map[string]any{
		"nonce":       nonce,
		"egress_port": 0,
		"upstream": map[string]any{
			"host":           host,
			"method":         req.Method,
			"path":           req.URL.RequestURI(),
			"headers":        headers,
			"headersOrdered": ordered,
		},
		"token": token,
	}
	payload, err := json.Marshal(head)
	return payload, requestID, err
}

func parseResponseHead(payload []byte) (int, http.Header, error) {
	var head struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return 0, nil, err
	}
	if head.Status < 100 || head.Status > 599 {
		return 0, nil, fmt.Errorf("invalid upstream status %d", head.Status)
	}
	headers := make(http.Header, len(head.Headers))
	for key, value := range head.Headers {
		headers.Set(key, value)
	}
	return head.Status, headers, nil
}

func randomBase64(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func safeFrameMessage(payload []byte) string {
	var value struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &value) == nil && value.Message != "" {
		return value.Message
	}
	return strings.TrimSpace(string(payload))
}

func isControlPlaneRequest(value *url.URL) bool {
	host := strings.ToLower(value.Hostname())
	switch host {
	case "oauth2.googleapis.com", "accounts.google.com", "auth.openai.com", "login.microsoftonline.com":
		return true
	}
	path := strings.ToLower(value.Path)
	return strings.Contains(path, "/oauth/") || strings.HasSuffix(path, "/token") || strings.Contains(path, "/auth/token")
}

type frameBody struct {
	source  io.ReadCloser
	record  *record
	cancel  context.CancelFunc
	pending []byte
	done    bool
}

func (b *frameBody) Read(p []byte) (int, error) {
	for {
		if len(b.pending) > 0 {
			n := copy(p, b.pending)
			b.pending = b.pending[n:]
			return n, nil
		}
		if b.done {
			return 0, io.EOF
		}
		typ, payload, err := readFrame(b.source)
		if err != nil {
			pooErr := &Error{Message: "PoO response ended before proof trailer", Cause: err, Submitted: true}
			b.record.finish(nil, pooErr)
			b.done = true
			return 0, pooErr
		}
		switch typ {
		case frameRespChunk:
			if len(payload) > 0 {
				b.pending = payload
			}
		case frameRespTrailer:
			if !json.Valid(payload) {
				pooErr := &Error{Message: "PoO proof trailer is invalid", Submitted: true}
				b.record.finish(nil, pooErr)
				b.done = true
				return 0, pooErr
			}
			b.record.finish(payload, nil)
			b.done = true
			return 0, io.EOF
		case frameError:
			pooErr := &Error{Message: "PoO enclave error: " + safeFrameMessage(payload), Submitted: true}
			b.record.finish(nil, pooErr)
			b.done = true
			return 0, pooErr
		default:
			pooErr := &Error{Message: fmt.Sprintf("unexpected PoO response frame 0x%02x", typ), Submitted: true}
			b.record.finish(nil, pooErr)
			b.done = true
			return 0, pooErr
		}
	}
}

func (b *frameBody) Close() error {
	// Executors may stop reading as soon as they observe an application-level
	// terminal event (for example response.completed). The PoO proof follows that
	// event in RESP_TRAILER, so drain the framed response before closing instead
	// of treating a normal early Close as a missing proof.
	var drainErr error
	if !b.done && b.source != nil {
		buffer := make([]byte, 32*1024)
		for !b.done {
			_, err := b.Read(buffer)
			if err == nil {
				continue
			}
			if errors.Is(err, io.EOF) {
				break
			}
			drainErr = err
			break
		}
	}
	if !b.done {
		drainErr = &Error{Message: "PoO response body closed before proof trailer", Submitted: true}
		b.record.finish(nil, drainErr)
		b.done = true
	}
	if b.cancel != nil {
		b.cancel()
	}
	var closeErr error
	if b.source != nil {
		closeErr = b.source.Close()
	}
	return errors.Join(drainErr, closeErr)
}
