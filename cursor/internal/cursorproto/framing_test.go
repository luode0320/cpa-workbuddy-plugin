package cursorproto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_FrameDecoder_decodes_fragmented_connect_frame(t *testing.T) {
	// Given
	payload := []byte("cursor")
	frame := EncodeConnectFrame(payload)
	decoder := NewFrameDecoder(1024)

	// When
	first, err := decoder.Push(frame[:3])
	require.NoError(t, err)
	second, err := decoder.Push(frame[3:])

	// Then
	require.NoError(t, err)
	require.Empty(t, first)
	require.Len(t, second, 1)
	require.Equal(t, payload, second[0].Payload)
	require.Equal(t, byte(0), second[0].Flags)
}

func Test_EncodeBidiAppend_uses_hex_compatibility_shape(t *testing.T) {
	// Given
	payload := []byte{0x01, 0xab}

	// When
	encoded, err := EncodeBidiAppend(payload, "request-1", 0)

	// Then
	require.NoError(t, err)
	require.Equal(t, []byte{0x0a, 0x04, '0', '1', 'a', 'b', 0x12, 0x0b, 0x0a, 0x09, 'r', 'e', 'q', 'u', 'e', 's', 't', '-', '1'}, encoded)
}
