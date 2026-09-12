package poo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const InternalRecordHeader = "X-CPA-Internal-PoO-Record"

var records sync.Map

type record struct {
	done  chan struct{}
	once  sync.Once
	proof json.RawMessage
	err   error
}

func newRecord(id string) *record {
	r := &record{done: make(chan struct{})}
	records.Store(id, r)
	time.AfterFunc(15*time.Minute, func() { records.Delete(id) })
	return r
}

func (r *record) finish(proof []byte, err error) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		if len(proof) > 0 {
			r.proof = append(json.RawMessage(nil), proof...)
		}
		r.err = err
		close(r.done)
	})
}

// TakeRecordID removes the internal transport header and returns its recorder id.
func TakeRecordID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	id := headers.Get(InternalRecordHeader)
	headers.Del(InternalRecordHeader)
	return id
}

// AwaitResult waits for the proof trailer and removes the recorder.
func AwaitResult(id string, timeout time.Duration) (json.RawMessage, error) {
	if id == "" {
		return nil, errors.New("PoO response record is missing")
	}
	value, ok := records.Load(id)
	if !ok {
		return nil, errors.New("PoO response record expired")
	}
	r := value.(*record)
	if timeout <= 0 {
		<-r.done
	} else {
		select {
		case <-r.done:
		case <-time.After(timeout):
			return nil, errors.New("timed out waiting for PoO proof trailer")
		}
	}
	records.Delete(id)
	if r.err != nil {
		return nil, r.err
	}
	if len(r.proof) == 0 || !json.Valid(r.proof) {
		return nil, errors.New("PoO proof trailer is missing or invalid")
	}
	return append(json.RawMessage(nil), r.proof...), nil
}

// Error is request-scoped: once submitted to the Gateway, it must not be replayed with another credential.
type Error struct {
	Message   string
	Cause     error
	Submitted bool
}

func (e *Error) Error() string {
	if e == nil {
		return "PoO gateway error"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error         { return e.Cause }
func (e *Error) StatusCode() int       { return http.StatusBadGateway }
func (e *Error) IsRequestScoped() bool { return e != nil && e.Submitted }

func IsError(err error) bool {
	var target *Error
	return errors.As(err, &target)
}
