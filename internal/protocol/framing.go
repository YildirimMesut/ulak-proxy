package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

const MaxPacketSize = 65535

// Frame types for the multiplexed TCP connection.
//
// Wire format: [1 type][2 len][payload]
//
// TypeTUN      payload = raw IP packet
// TypeFwdOpen  payload = [4 streamID][2 clientPort]   (server→agent)
// TypeFwdData  payload = [4 streamID][data…]          (bidirectional)
// TypeFwdClose payload = [4 streamID]                 (bidirectional)
const (
	TypeTUN      byte = 0x01
	TypeFwdOpen  byte = 0x02
	TypeFwdData  byte = 0x03
	TypeFwdClose byte = 0x04
)

// BuildFrame serializes a frame into a byte slice without writing to a Writer.
// Used by the writer-goroutine pattern to avoid mutex contention.
func BuildFrame(frameType byte, payload []byte) []byte {
	buf := make([]byte, 3+len(payload))
	buf[0] = frameType
	buf[1] = byte(len(payload) >> 8)
	buf[2] = byte(len(payload))
	copy(buf[3:], payload)
	return buf
}

func WriteFrame(w io.Writer, frameType byte, payload []byte) error {
	if len(payload) > MaxPacketSize {
		return fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), MaxPacketSize)
	}
	buf := make([]byte, 3+len(payload))
	buf[0] = frameType
	buf[1] = byte(len(payload) >> 8)
	buf[2] = byte(len(payload))
	copy(buf[3:], payload)
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

func ReadFrame(r io.Reader) (frameType byte, payload []byte, err error) {
	hdr := make([]byte, 3)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return 0, nil, fmt.Errorf("read header: %w", err)
	}
	frameType = hdr[0]
	length := int(binary.BigEndian.Uint16(hdr[1:3]))
	if length == 0 {
		return frameType, []byte{}, nil
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, fmt.Errorf("read payload: %w", err)
	}
	return frameType, payload, nil
}

func WriteTUN(w io.Writer, pkt []byte) error {
	return WriteFrame(w, TypeTUN, pkt)
}

func WritePacket(w io.Writer, data []byte) error { return WriteTUN(w, data) }

func WriteFwdOpen(w io.Writer, streamID uint32, clientPort uint16) error {
	payload := make([]byte, 6)
	binary.BigEndian.PutUint32(payload[0:4], streamID)
	binary.BigEndian.PutUint16(payload[4:6], clientPort)
	return WriteFrame(w, TypeFwdOpen, payload)
}

func WriteFwdData(w io.Writer, streamID uint32, data []byte) error {
	payload := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(payload[0:4], streamID)
	copy(payload[4:], data)
	return WriteFrame(w, TypeFwdData, payload)
}

func WriteFwdClose(w io.Writer, streamID uint32) error {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload[0:4], streamID)
	return WriteFrame(w, TypeFwdClose, payload)
}

func ParseFwdOpen(payload []byte) (streamID uint32, port uint16, err error) {
	if len(payload) < 6 {
		return 0, 0, fmt.Errorf("fwd open payload too short (%d)", len(payload))
	}
	return binary.BigEndian.Uint32(payload[0:4]), binary.BigEndian.Uint16(payload[4:6]), nil
}

func ParseFwdData(payload []byte) (streamID uint32, data []byte, err error) {
	if len(payload) < 4 {
		return 0, nil, fmt.Errorf("fwd data payload too short (%d)", len(payload))
	}
	return binary.BigEndian.Uint32(payload[0:4]), payload[4:], nil
}

func ParseFwdClose(payload []byte) (streamID uint32, err error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("fwd close payload too short (%d)", len(payload))
	}
	return binary.BigEndian.Uint32(payload[0:4]), nil
}
