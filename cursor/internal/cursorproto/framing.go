package cursorproto

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

var ErrFrameTooLarge = errors.New("Cursor Connect frame exceeds configured limit")

type Frame struct {
	Flags   byte
	Payload []byte
}

type FrameDecoder struct {
	buffer   []byte
	maxBytes uint32
}

func NewFrameDecoder(maxBytes uint32) *FrameDecoder {
	return &FrameDecoder{maxBytes: maxBytes}
}

func EncodeConnectFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func (decoder *FrameDecoder) Push(chunk []byte) ([]Frame, error) {
	decoder.buffer = append(decoder.buffer, chunk...)
	frames := make([]Frame, 0, 1)
	for len(decoder.buffer) >= 5 {
		payloadBytes := binary.BigEndian.Uint32(decoder.buffer[1:5])
		if payloadBytes > decoder.maxBytes {
			return nil, fmt.Errorf("%w: %d", ErrFrameTooLarge, payloadBytes)
		}
		frameBytes := 5 + int(payloadBytes)
		if len(decoder.buffer) < frameBytes {
			break
		}
		payload := append([]byte(nil), decoder.buffer[5:frameBytes]...)
		frames = append(frames, Frame{Flags: decoder.buffer[0], Payload: payload})
		decoder.buffer = decoder.buffer[frameBytes:]
	}
	return frames, nil
}

func EncodeBidiAppend(payload []byte, requestID string, sequence uint64) ([]byte, error) {
	if requestID == "" {
		return nil, errors.New("Cursor request ID is required")
	}
	requestIDMessage := protowire.AppendTag(nil, 1, protowire.BytesType)
	requestIDMessage = protowire.AppendString(requestIDMessage, requestID)
	encoded := make([]byte, 0, len(payload)*2+len(requestID)+16)
	if len(payload) > 0 {
		encoded = protowire.AppendTag(encoded, 1, protowire.BytesType)
		encoded = protowire.AppendString(encoded, hex.EncodeToString(payload))
	}
	encoded = protowire.AppendTag(encoded, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, requestIDMessage)
	if sequence > 0 {
		encoded = protowire.AppendTag(encoded, 3, protowire.VarintType)
		encoded = protowire.AppendVarint(encoded, sequence)
	}
	return encoded, nil
}
