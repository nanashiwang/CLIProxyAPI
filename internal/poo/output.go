package poo

import (
	"bytes"
	"encoding/json"
	"errors"
)

var streamMarker = []byte{0, 'C', 'P', 'A', '-', 'P', 'O', 'O', 0}

type streamEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func InjectProof(body, proof []byte) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("response JSON is not an object")
		}
		return nil, err
	}
	if !json.Valid(proof) {
		return nil, errors.New("proof JSON is invalid")
	}
	object["proof"] = append(json.RawMessage(nil), proof...)
	return json.Marshal(object)
}

func EncodeStreamEvent(event string, data []byte) []byte {
	if !json.Valid(data) {
		data, _ = json.Marshal(map[string]any{"message": string(data)})
	}
	payload, _ := json.Marshal(streamEvent{Event: event, Data: append(json.RawMessage(nil), data...)})
	return append(append([]byte(nil), streamMarker...), payload...)
}

func DecodeStreamEvent(chunk []byte) (event string, data json.RawMessage, ok bool) {
	if !bytes.HasPrefix(chunk, streamMarker) {
		return "", nil, false
	}
	var value streamEvent
	if json.Unmarshal(chunk[len(streamMarker):], &value) != nil || value.Event == "" || !json.Valid(value.Data) {
		return "", nil, false
	}
	return value.Event, value.Data, true
}
