package auth

import (
	"encoding/json"
	"fmt"
)

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringFromMap(in map[string]any, key string) string {
	if in == nil {
		return ""
	}
	value, ok := in[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func mapToStruct(in map[string]any, out any) error {
	raw, errMarshal := json.Marshal(in)
	if errMarshal != nil {
		return fmt.Errorf("encode gemini-cli auth map: %w", errMarshal)
	}
	if errUnmarshal := json.Unmarshal(raw, out); errUnmarshal != nil {
		return fmt.Errorf("decode gemini-cli auth map: %w", errUnmarshal)
	}
	return nil
}

func tokenMapFromTopLevel(in map[string]any) map[string]any {
	out := make(map[string]any)
	for _, key := range []string{"access_token", "refresh_token", "token_type", "expiry", "expires_in", "scope"} {
		if value, ok := in[key]; ok {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
