package helps

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// BeginModelObservation starts a new upstream exchange. Retry attempts must not
// inherit an earlier response's model, even when the next attempt fails to dial.
func (r *UsageReporter) BeginModelObservation(payload []byte) {
	if r == nil {
		return
	}
	generation := r.resetModelObservation()
	if model := gjson.GetBytes(payload, "model"); model.Type == gjson.String {
		r.observeUpstreamModel(model.String(), generation)
	}
}

func (r *UsageReporter) resetModelObservation() uint64 {
	r.modelMu.Lock()
	defer r.modelMu.Unlock()
	r.modelGeneration++
	r.upstreamModel, r.responseModel, r.responseSource = "", "", ""
	r.upstreamTransport = ""
	r.responseRank = 0
	return r.modelGeneration
}

func (r *UsageReporter) observeUpstreamModel(model string, generation uint64) {
	model = usage.NormalizeObservedModel(model)
	if model == "" {
		return
	}
	r.modelMu.Lock()
	defer r.modelMu.Unlock()
	if generation == r.modelGeneration {
		r.upstreamModel = model
	}
}

func (r *UsageReporter) observeHTTPRequestModel(req *http.Request) uint64 {
	generation := r.resetModelObservation()
	if req == nil {
		return generation
	}
	r.observeUpstreamTransport("http", generation)
	// Gemini and Vertex name the model in the actual endpoint, not the JSON body.
	if req.URL != nil {
		path := req.URL.Path
		if index := strings.LastIndex(path, "/models/"); index >= 0 {
			modelAction := path[index+len("/models/"):]
			if model, action, ok := strings.Cut(modelAction, ":"); ok && !strings.Contains(model, "/") {
				switch action {
				case "generateContent", "streamGenerateContent", "predict", "predictLongRunning":
					r.observeUpstreamModel(model, generation)
					return generation
				}
			}
		}
	}
	if req.GetBody == nil || strings.Contains(req.Header.Get("Content-Type"), "multipart/") {
		return generation
	}
	// GetBody leaves the transport's reader, Content-Length and replay semantics
	// untouched. The parser retains only bounded field names and model strings.
	body, err := req.GetBody()
	if err != nil || body == nil {
		return generation
	}
	defer func() { _ = body.Close() }()
	parser := modelJSONObserver{request: true, emit: func(model, _ string, _ uint8) {
		r.observeUpstreamModel(model, generation)
	}}
	// A custom GetBody implementation must not turn observation into an unbounded
	// extra read. Common executors put the model before their large input arrays.
	reader := io.LimitReader(body, 1<<20)
	buffer := make([]byte, 4096)
	for {
		n, errRead := reader.Read(buffer)
		if n > 0 {
			parser.Feed(buffer[:n])
		}
		if errRead != nil || n == 0 || parser.model != "" {
			break
		}
	}
	return generation
}

// BeginHTTPModelObservation observes the exact HTTP request forwarded through a
// non-HTTP transport, such as AI Studio's websocket relay.
func (r *UsageReporter) BeginHTTPModelObservation(req *http.Request) {
	if r != nil {
		r.observeHTTPRequestModel(req)
	}
}

// NewResponseModelStreamObserver observes raw response chunks delivered by a
// non-HTTP transport, retaining the same bounded parser and exchange isolation.
func (r *UsageReporter) NewResponseModelStreamObserver(contentType string) (func([]byte), func()) {
	if r == nil {
		return func([]byte) {}, func() {}
	}
	r.modelMu.RLock()
	generation := r.modelGeneration
	r.modelMu.RUnlock()
	observer := newResponseModelObserver(contentType, func(model, source string, rank uint8) {
		r.observeResponseModel(model, source, rank, generation)
	})
	return observer.Feed, observer.Finish
}

// ObserveResponseModelHeaders accepts only explicit model-identification headers.
// A reused websocket's old handshake headers must not be passed here again.
func (r *UsageReporter) ObserveResponseModelHeaders(headers http.Header) {
	if r == nil {
		return
	}
	r.modelMu.RLock()
	generation := r.modelGeneration
	r.modelMu.RUnlock()
	r.observeResponseModelHeaders(headers, generation)
}

func (r *UsageReporter) observeResponseModelHeaders(headers http.Header, generation uint64) {
	for _, key := range []string{"Openai-Model", "X-Openai-Model"} {
		for _, value := range headers.Values(key) {
			if model := usage.NormalizeObservedModel(value); model != "" {
				r.observeResponseModel(model, "header", 3, generation)
				return
			}
		}
	}
}

// ObserveResponseModelPayload reads an unmodified upstream websocket/event JSON
// payload. It never inspects user/tool content or a downstream translated body.
func (r *UsageReporter) ObserveResponseModelPayload(payload []byte, source string) {
	if r == nil || (source != "body" && source != "metadata") || !gjson.ValidBytes(payload) {
		return
	}
	r.modelMu.RLock()
	generation := r.modelGeneration
	r.modelMu.RUnlock()
	parser := modelJSONObserver{source: source, emit: func(model, observedSource string, rank uint8) {
		r.observeResponseModel(model, observedSource, rank, generation)
	}}
	parser.Feed(payload)
	parser.Finish()
}

// ObserveDecodedResponseBody observes providers that explicitly decompress their
// response after RoundTrip. Decompression and transport ownership stay with the
// executor; this wrapper reads no bytes ahead and adds no buffering or timeout.
func (r *UsageReporter) ObserveDecodedResponseBody(body io.ReadCloser, contentType string) io.ReadCloser {
	if r == nil || body == nil {
		return body
	}
	r.modelMu.RLock()
	generation := r.modelGeneration
	r.modelMu.RUnlock()
	observer := newResponseModelObserver(contentType, func(model, source string, rank uint8) {
		r.observeResponseModel(model, source, rank, generation)
	})
	return &usageTTFTReadCloser{ReadCloser: body, observe: observer.Feed, finish: observer.Finish}
}

func (r *UsageReporter) observeResponseModel(model, source string, rank uint8, generation uint64) {
	model = usage.NormalizeObservedModel(model)
	if model == "" {
		return
	}
	r.modelMu.Lock()
	defer r.modelMu.Unlock()
	if generation != r.modelGeneration || rank < r.responseRank {
		return
	}
	r.responseModel, r.responseSource, r.responseRank = model, source, rank
}

// modelJSONObserver is a bounded streaming projection, not a body buffer. The
// only retained strings are protocol field names and potential model values;
// large text, image data, tool arguments and nested objects are discarded.
type modelJSONObserver struct {
	request                           bool
	source                            string
	emit                              func(string, string, uint8)
	frames                            [64]modelJSONFrame
	depth                             int
	started, complete, invalid        bool
	inString, escaped, isKey, capture bool
	unicodeDigits                     int
	token                             []byte
	scalar                            []byte
	field, eventType, header, model   string
}

type modelJSONFrame struct {
	kind  byte
	scope string
	key   string
	state uint8
}

const (
	jsonKeyOrEnd uint8 = iota
	jsonColon
	jsonValue
	jsonObjectComma
	jsonKey
	jsonValueOrEnd
	jsonArrayValue
	jsonArrayComma
)

func (p *modelJSONObserver) consumeValue() bool {
	if p.depth == 0 {
		return !p.started
	}
	frame := &p.frames[p.depth-1]
	switch frame.state {
	case jsonValue:
		frame.state = jsonObjectComma
	case jsonValueOrEnd, jsonArrayValue:
		frame.state = jsonArrayComma
	default:
		return false
	}
	return true
}

func (p *modelJSONObserver) Feed(data []byte) {
	for _, char := range data {
		if p.invalid {
			return
		}
		if p.inString {
			if p.capture {
				if len(p.token) < 2048 {
					p.token = append(p.token, char)
				} else {
					p.capture = false
					p.token = p.token[:0]
				}
			}
			if p.unicodeDigits > 0 {
				if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
					p.invalid = true
					return
				}
				p.unicodeDigits--
			} else if p.escaped {
				p.escaped = false
				switch char {
				case 'u':
					p.unicodeDigits = 4
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				default:
					p.invalid = true
					return
				}
			} else if char == '\\' {
				p.escaped = true
			} else if char == '"' {
				p.inString = false
				p.finishString()
			} else if char < 0x20 {
				p.invalid = true
				return
			}
			continue
		}
		if len(p.scalar) > 0 {
			if char != ',' && char != '}' && char != ']' && char != ' ' && char != '\t' && char != '\r' && char != '\n' {
				if len(p.scalar) == 128 {
					p.invalid = true
					return
				}
				p.scalar = append(p.scalar, char)
				continue
			}
			if !json.Valid(p.scalar) {
				p.invalid = true
				return
			}
			p.scalar = p.scalar[:0]
		}
		if char == ' ' || char == '\t' || char == '\r' || char == '\n' {
			continue
		}
		if p.complete {
			p.invalid = true
			return
		}
		if char == '"' {
			if p.depth == 0 {
				p.invalid = true
				return
			}
			frame := &p.frames[p.depth-1]
			p.isKey = frame.state == jsonKeyOrEnd || frame.state == jsonKey
			if !p.isKey && !p.consumeValue() {
				p.invalid = true
				return
			}
			p.inString, p.capture, p.field = true, frame.scope != "" && p.isKey, ""
			if !p.isKey {
				p.field = p.modelField(*frame)
				p.capture = p.field != ""
			}
			p.token = p.token[:0]
			if p.capture {
				p.token = append(p.token, '"')
			}
			continue
		}
		switch char {
		case '{', '[':
			if p.depth == len(p.frames) || !p.consumeValue() {
				p.invalid = true
				return
			}
			scope := ""
			if p.depth == 0 {
				p.started = true
				if char == '{' {
					scope = "root"
				} else if !p.request {
					scope = "rootArray"
				}
			} else {
				parent := p.frames[p.depth-1]
				if parent.scope == "rootArray" && char == '{' {
					scope = "root"
				} else if !p.request && parent.scope == "root" && char == '{' {
					switch parent.key {
					case "response", "message", "interaction", "headers":
						scope = parent.key
					}
				} else if !p.request && parent.scope == "response" && char == '{' && parent.key == "headers" {
					scope = "headers"
				} else if !p.request && parent.scope == "headers" && char == '[' && isModelHeader(parent.key) {
					scope = "headerArray"
				}
			}
			state := jsonKeyOrEnd
			if char == '[' {
				state = jsonValueOrEnd
			}
			p.frames[p.depth] = modelJSONFrame{kind: char, scope: scope, state: state}
			p.depth++
		case '}', ']':
			if p.depth == 0 {
				p.invalid = true
				return
			}
			frame := p.frames[p.depth-1]
			valid := char == '}' && frame.kind == '{' && (frame.state == jsonKeyOrEnd || frame.state == jsonObjectComma)
			valid = valid || char == ']' && frame.kind == '[' && (frame.state == jsonValueOrEnd || frame.state == jsonArrayComma)
			if !valid {
				p.invalid = true
				return
			}
			p.depth--
			if p.depth == 0 {
				p.complete = true
			}
		case ':':
			if p.depth == 0 || p.frames[p.depth-1].state != jsonColon {
				p.invalid = true
				return
			}
			p.frames[p.depth-1].state = jsonValue
		case ',':
			if p.depth == 0 {
				p.invalid = true
				return
			}
			frame := &p.frames[p.depth-1]
			switch frame.state {
			case jsonObjectComma:
				frame.state = jsonKey
			case jsonArrayComma:
				frame.state = jsonArrayValue
			default:
				p.invalid = true
				return
			}
			frame.key = ""
		default:
			if p.depth == 0 || !p.consumeValue() {
				p.invalid = true
				return
			}
			if char != '-' && char != 't' && char != 'f' && char != 'n' && (char < '0' || char > '9') {
				p.invalid = true
				return
			}
			p.scalar = append(p.scalar[:0], char)
		}
	}
}

func isModelHeader(key string) bool {
	return strings.EqualFold(key, "openai-model") || strings.EqualFold(key, "x-openai-model")
}

func (p *modelJSONObserver) modelField(frame modelJSONFrame) string {
	if frame.scope == "root" {
		if frame.key == "model" {
			return "model"
		}
		if !p.request && frame.key == "modelVersion" {
			return "model"
		}
		if !p.request && frame.key == "type" {
			return "type"
		}
	}
	if p.request {
		return ""
	}
	if (frame.scope == "response" || frame.scope == "message" || frame.scope == "interaction") && (frame.key == "model" || frame.key == "modelVersion") {
		return "model"
	}
	if (frame.scope == "headers" && isModelHeader(frame.key)) || frame.scope == "headerArray" {
		return "header"
	}
	return ""
}

func (p *modelJSONObserver) finishString() {
	if p.isKey {
		frame := &p.frames[p.depth-1]
		frame.key, frame.state = "", jsonColon
	}
	if !p.capture {
		return
	}
	var value string
	if json.Unmarshal(p.token, &value) != nil {
		p.invalid = true
		return
	}
	if p.isKey {
		p.frames[p.depth-1].key = usage.NormalizeObservedModel(value)
		return
	}
	switch p.field {
	case "type":
		p.eventType = value
	case "header":
		p.header = usage.NormalizeObservedModel(value)
	case "model":
		p.model = usage.NormalizeObservedModel(value)
		if p.request && p.model != "" {
			p.emit(p.model, "", 0)
		}
	}
}

// Finish commits only a complete, valid JSON document. SSE consumers may publish
// usage as soon as a data line completes, so validation follows that existing
// line-based boundary rather than delaying publication until the event separator.
func (p *modelJSONObserver) Finish() {
	if p.invalid || !p.complete || p.inString || p.depth != 0 || len(p.scalar) != 0 {
		return
	}
	if p.header != "" && (strings.HasPrefix(p.eventType, "response.") || p.eventType == "codex.response.metadata") {
		p.emit(p.header, "metadata", 3)
	} else if p.model != "" {
		source := p.source
		if source == "" {
			source = "body"
		}
		p.emit(p.model, source, 1)
	}
}

type responseModelObserver struct {
	parser      modelJSONObserver
	sse         bool
	detected    bool
	prefix      []byte
	dataLine    bool
	lineStarted bool
}

func newResponseModelObserver(contentType string, emit func(string, string, uint8)) *responseModelObserver {
	sse := strings.Contains(strings.ToLower(contentType), "text/event-stream")
	return &responseModelObserver{parser: modelJSONObserver{emit: emit}, sse: sse, detected: sse}
}

func (o *responseModelObserver) Feed(data []byte) {
	if !o.detected {
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) == 0 {
			return
		}
		o.sse = trimmed[0] != '{' && trimmed[0] != '['
		o.detected = true
	}
	if !o.sse {
		o.parser.Feed(data)
		return
	}
	// Feed only SSE data lines. Comments and event names are never interpreted as
	// JSON. Incomplete or malformed JSON data lines are discarded at the next
	// event separator; complete data documents follow the executor's line boundary.
	for _, char := range data {
		if char == '\n' {
			if !o.lineStarted {
				o.parser.Finish()
				o.parser = modelJSONObserver{emit: o.parser.emit}
			} else if o.dataLine {
				o.parser.Feed([]byte{'\n'})
				o.parser.Finish()
			}
			o.prefix, o.dataLine, o.lineStarted = o.prefix[:0], false, false
			continue
		}
		if char == '\r' {
			continue
		}
		o.lineStarted = true
		if o.dataLine {
			o.parser.Feed([]byte{char})
			continue
		}
		if len(o.prefix) < 5 {
			o.prefix = append(o.prefix, char)
			if len(o.prefix) == 5 && string(o.prefix) == "data:" {
				o.dataLine = true
			}
		}
	}
}

func (o *responseModelObserver) Finish() { o.parser.Finish() }
