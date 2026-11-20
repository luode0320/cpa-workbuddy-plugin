package tokencount

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkEndSplitsLongSingleLineOnValidUTF8Boundary(t *testing.T) {
	// Construct a single-line multi-byte CJK string > 40 KiB with zero newlines.
	input := strings.Repeat("深度学习与多模态模型架构优化", 1200)
	if len(input) <= maxChunkBytes*2 {
		t.Fatalf("expected test input > %d bytes, got %d", maxChunkBytes*2, len(input))
	}

	var rebuilt strings.Builder
	rest := input
	chunksCount := 0
	for rest != "" {
		end := chunkEnd(rest)
		if end <= 0 || end > maxChunkBytes {
			t.Fatalf("unexpected chunkEnd=%d (max=%d)", end, maxChunkBytes)
		}
		part := rest[:end]
		if !utf8.ValidString(part) {
			t.Fatalf("chunk %d broke UTF-8 rune boundary", chunksCount)
		}
		rebuilt.WriteString(part)
		rest = rest[end:]
		chunksCount++
	}
	if rebuilt.String() != input {
		t.Fatal("rebuilt chunks did not equal original input")
	}
	if tokens := Text(input); tokens <= 0 {
		t.Fatalf("expected positive token count, got %d", tokens)
	}
}
