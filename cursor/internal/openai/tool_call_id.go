package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxToolCallIDBytes = 64

func (turn *Turn) reserveToolCallID(raw string) (string, error) {
	id, err := canonicalToolCallID(raw)
	if err != nil {
		return "", err
	}
	if turn.toolIDOwners == nil {
		turn.toolIDOwners = make(map[string]string)
	}
	if owner, exists := turn.toolIDOwners[id]; exists {
		if owner == raw {
			return "", errors.New("duplicate Cursor tool call id")
		}
		return "", errors.New("Cursor tool call id normalization collision")
	}
	turn.toolIDOwners[id] = raw
	return id, nil
}

func canonicalToolCallID(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("Cursor tool call id is empty or contains only whitespace")
	}
	if validToolCallID(raw) {
		return raw, nil
	}
	return hashedToolCallID(raw), nil
}

func validToolCallID(id string) bool {
	if strings.TrimSpace(id) == "" || len(id) > maxToolCallIDBytes || !utf8.ValidString(id) {
		return false
	}
	for _, character := range id {
		if unicode.IsControl(character) || unicode.In(character, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

func hashedToolCallID(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return "call_cursor_" + hex.EncodeToString(digest[:26])
}
