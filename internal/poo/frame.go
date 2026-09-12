package poo

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	frameReqHead     byte = 0x01
	frameReqBody     byte = 0x02
	frameRespHead    byte = 0x10
	frameRespChunk   byte = 0x11
	frameRespTrailer byte = 0x12
	frameError       byte = 0x1f
	frameHeaderSize       = 5
	maxResponseFrame      = 64 * 1024 * 1024
)

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	var header [frameHeaderSize]byte
	header[0] = typ
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var header [frameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxResponseFrame {
		return 0, nil, fmt.Errorf("frame length %d exceeds limit", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}
