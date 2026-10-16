package logging

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

var logDropped, logErrorsDropped, logWriteErrors, streamDropped, forwardDropped atomic.Uint64
var activeAsyncOutput atomic.Pointer[boundedLogWriter]

// LogHealth describes operational logging only, never billable usage delivery.
type LogHealth struct {
	DroppedRecords       uint64 `json:"dropped_records"`
	DroppedErrors        uint64 `json:"dropped_errors"`
	WriteErrors          uint64 `json:"write_errors"`
	StreamChunksDropped  uint64 `json:"stream_chunks_dropped"`
	ForwardedLogsDropped uint64 `json:"forwarded_logs_dropped"`
	QueuedRecords        int    `json:"queued_records"`
	QueuedBytes          int    `json:"queued_bytes"`
}

func OperationalLogHealth() LogHealth {
	h := LogHealth{DroppedRecords: logDropped.Load(), DroppedErrors: logErrorsDropped.Load(), WriteErrors: logWriteErrors.Load(), StreamChunksDropped: streamDropped.Load(), ForwardedLogsDropped: forwardDropped.Load()}
	if w := activeAsyncOutput.Load(); w != nil {
		w.mu.Lock()
		h.QueuedRecords = w.items
		h.QueuedBytes = w.bytes
		w.mu.Unlock()
	}
	return h
}

type queuedLog struct {
	data    []byte
	reserve bool
}
type boundedLogWriter struct {
	mu                                         sync.Mutex
	queue                                      chan queuedLog
	done                                       chan struct{}
	closed                                     bool
	items, bytes, reserveItems, reserveBytes   int
	maxItems, maxBytes, errorItems, errorBytes int
}

func newBoundedLogWriter(sink io.Writer, items, bytes, errorItems, errorBytes int) *boundedLogWriter {
	w := &boundedLogWriter{queue: make(chan queuedLog, items+errorItems), done: make(chan struct{}), maxItems: items, maxBytes: bytes, errorItems: errorItems, errorBytes: errorBytes}
	go w.run(sink)
	return w
}

func (w *boundedLogWriter) Write(p []byte) (int, error) {
	// The formatter's third bracket is the level. Message contents cannot
	// promote an ordinary record into the warning/error reserve.
	parts := strings.SplitN(string(p[:min(len(p), 128)]), "] ", 4)
	important := len(parts) > 2 && (strings.TrimSpace(parts[2]) == "[error" || strings.TrimSpace(parts[2]) == "[warn" || strings.TrimSpace(parts[2]) == "[fatal" || strings.TrimSpace(parts[2]) == "[panic")
	w.enqueue(p, important)
	// Returning an error makes logrus synchronously fall back to stderr.
	// Explicit counters report completeness without blocking request delivery.
	return len(p), nil
}

func (w *boundedLogWriter) enqueue(p []byte, important bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	reserve := w.items-w.reserveItems >= w.maxItems || w.bytes-w.reserveBytes+len(p) > w.maxBytes
	if w.closed || (reserve && (!important || w.reserveItems >= w.errorItems || w.reserveBytes+len(p) > w.errorBytes)) {
		logDropped.Add(1)
		if important {
			logErrorsDropped.Add(1)
		}
		return
	}
	w.items++
	w.bytes += len(p)
	if reserve {
		w.reserveItems++
		w.reserveBytes += len(p)
	}
	w.queue <- queuedLog{data: append([]byte(nil), p...), reserve: reserve}
}

func (w *boundedLogWriter) run(sink io.Writer) {
	defer close(w.done)
	writer := bufio.NewWriterSize(sink, 32<<10)
	for first := range w.queue {
		batch := []queuedLog{first}
	collect:
		for len(batch) < 64 {
			select {
			case item, ok := <-w.queue:
				if !ok {
					break collect
				}
				batch = append(batch, item)
			default:
				break collect
			}
		}
		failed := false
		for _, item := range batch {
			if _, err := writer.Write(item.data); err != nil {
				failed = true
			}
		}
		if err := writer.Flush(); err != nil {
			failed = true
		}
		if failed {
			logWriteErrors.Add(uint64(len(batch)))
			writer.Reset(sink)
		}
		// In-flight writes retain their budget until the sink flush completes.
		w.mu.Lock()
		for _, item := range batch {
			w.items--
			w.bytes -= len(item.data)
			if item.reserve {
				w.reserveItems--
				w.reserveBytes -= len(item.data)
			}
		}
		w.mu.Unlock()
	}
}

func (w *boundedLogWriter) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	<-w.done
}
