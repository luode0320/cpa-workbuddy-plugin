package main

import (
	"encoding/json"
	"testing"
)

// TestStripAnomalyKey covers the three shapes the legacy-flag purge can hit:
// a doc carrying the dead key (stripped, other keys intact), a doc without
// it (returned unchanged), and unparsable input (returned unchanged).
func TestStripAnomalyKey(t *testing.T) {
	// 1. Key present: stripped, every other top-level key preserved.
	withKey := []byte(`{"cred":{"token":"t"},"disabled":true,"anomaly":true,"note":"x","success_count":3}`)
	out, changed := stripAnomalyKey(withKey)
	if !changed {
		t.Fatal("doc carrying anomaly key must report changed")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("stripped output must be valid json: %v", err)
	}
	if _, ok := m["anomaly"]; ok {
		t.Fatal("anomaly key must be gone after strip")
	}
	for _, k := range []string{"cred", "disabled", "note", "success_count"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("key %q must survive the strip", k)
		}
	}

	// 2. Key absent: byte-for-byte unchanged.
	clean := []byte(`{"cred":{"token":"t"},"disabled":false}`)
	out2, changed2 := stripAnomalyKey(clean)
	if changed2 {
		t.Fatal("doc without anomaly key must report unchanged")
	}
	if string(out2) != string(clean) {
		t.Fatal("doc without anomaly key must be returned as-is")
	}

	// 3. Unparsable input: returned unchanged, never blind-written.
	bad := []byte(`{not json`)
	out3, changed3 := stripAnomalyKey(bad)
	if changed3 {
		t.Fatal("unparsable input must report unchanged")
	}
	if string(out3) != string(bad) {
		t.Fatal("unparsable input must be returned as-is")
	}

	// 4. JSON null doc: treated as no-op (doc parses to nil).
	nullDoc := []byte(`null`)
	if _, changed := stripAnomalyKey(nullDoc); changed {
		t.Fatal("null doc must report unchanged")
	}
}
