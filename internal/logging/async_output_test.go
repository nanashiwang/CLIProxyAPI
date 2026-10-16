package logging

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

type stalledLogSink struct {
	entered, release chan struct{}
	once             sync.Once
	data             bytes.Buffer
}

func (s *stalledLogSink) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.data.Write(p)
}

func TestBoundedLoggingKeepsInFlightBudgetAndErrorReserve(t *testing.T) {
	sink := &stalledLogSink{entered: make(chan struct{}), release: make(chan struct{})}
	w := newBoundedLogWriter(sink, 1, 64, 1, 64)
	before := OperationalLogHealth()
	w.enqueue([]byte("first"), false)
	<-sink.entered
	w.enqueue([]byte("ordinary dropped"), false)
	w.enqueue([]byte("error retained"), true)
	w.enqueue([]byte("error dropped"), true)
	w.mu.Lock()
	items, size := w.items, w.bytes
	w.mu.Unlock()
	if items != 2 || size != len("firsterror retained") {
		t.Fatalf("in-flight budget lost: %d/%d", items, size)
	}
	after := OperationalLogHealth()
	if after.DroppedRecords-before.DroppedRecords != 2 || after.DroppedErrors-before.DroppedErrors != 1 {
		t.Fatal("log loss not visible")
	}
	close(sink.release)
	w.Close()
	if sink.data.String() != "firsterror retained" {
		t.Fatalf("drain lost accepted data: %q", sink.data.String())
	}
	w.enqueue([]byte("closed"), false)
}

type failedLogSink struct{}

func (failedLogSink) Write([]byte) (int, error) { return 0, errors.New("synthetic sink failure") }
func TestLoggingWriteFailureIsObservable(t *testing.T) {
	before := OperationalLogHealth().WriteErrors
	w := newBoundedLogWriter(failedLogSink{}, 1, 64, 1, 64)
	w.enqueue([]byte("data"), true)
	w.Close()
	if OperationalLogHealth().WriteErrors <= before {
		t.Fatal("write failure was hidden")
	}
}
