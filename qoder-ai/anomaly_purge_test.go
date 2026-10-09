package main

import (
	"encoding/json"
	"testing"
)

// TestStripAnomalyKey locks the pure-fold behavior of the legacy anomaly
// flag purge (2026-09-08 anomaly-pool removal): the top-level "anomaly" key
// is deleted, every other field survives, and unparsable input is returned
// unchanged (the purge never blind-writes).
func TestStripAnomalyKey(t *testing.T) {
	t.Run("strips key and keeps siblings", func(t *testing.T) {
		raw := []byte(`{"anomaly":true,"disabled":false,"success_count":3,"note":"keep me"}`)
		out, changed := stripAnomalyKey(raw)
		if !changed {
			t.Fatal("expected changed=true when anomaly key present")
		}
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("output must stay valid JSON: %v", err)
		}
		if _, ok := doc["anomaly"]; ok {
			t.Fatal("anomaly key must be gone")
		}
		if doc["disabled"] != false || doc["note"] != "keep me" {
			t.Fatalf("sibling fields must survive, got %v", doc)
		}
		// success_count survives regardless of numeric type after a JSON round-trip.
		if v, ok := doc["success_count"].(float64); !ok || v != 3 {
			t.Fatalf("success_count must survive, got %v", doc["success_count"])
		}
	})

	t.Run("no key returns input unchanged", func(t *testing.T) {
		raw := []byte(`{"disabled":false,"success_count":1}`)
		out, changed := stripAnomalyKey(raw)
		if changed {
			t.Fatal("expected changed=false when no anomaly key")
		}
		if string(out) != string(raw) {
			t.Fatalf("output must be byte-identical, got %s", out)
		}
	})

	t.Run("malformed JSON returned unchanged", func(t *testing.T) {
		raw := []byte(`{"anomaly":true,`)
		out, changed := stripAnomalyKey(raw)
		if changed {
			t.Fatal("malformed JSON must never be rewritten")
		}
		if string(out) != string(raw) {
			t.Fatalf("malformed input must pass through untouched, got %s", out)
		}
	})

	t.Run("json null is a no-op", func(t *testing.T) {
		raw := []byte(`null`)
		out, changed := stripAnomalyKey(raw)
		if changed {
			t.Fatal("null document must be a no-op")
		}
		if string(out) != string(raw) {
			t.Fatalf("null must pass through untouched, got %s", out)
		}
	})
}
