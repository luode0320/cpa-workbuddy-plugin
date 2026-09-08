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

// TestStripLegacyDisabledFlag covers the legacy session-dead disable-pair
// purge: disabled:true + "Session expired" note is re-enabled (the note is
// kept — it truthfully describes the refresh token), while disabled docs
// with any other/absent note and non-disabled docs are never touched. The
// production fingerprints come from the 2026-09-08 22:00 incident where
// leaked ≤0.1.54 keepalive goroutines re-disabled three accounts.
func TestStripLegacyDisabledFlag(t *testing.T) {
	// 1. Legacy pair (exact production fingerprint): re-enabled, note kept,
	//    every other key preserved.
	legacy := []byte(`{"cred":{"token":"t"},"disabled":true,"note":"Session expired (refresh token dead): re-login required","success_count":3}`)
	out, changed := stripLegacyDisabledFlag(legacy)
	if !changed {
		t.Fatal("legacy session-dead pair must report changed")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("purged output must be valid json: %v", err)
	}
	if m["disabled"] != false {
		t.Fatal("legacy disabled flag must be cleared to false")
	}
	if m["note"] != "Session expired (refresh token dead): re-login required" {
		t.Fatal("note must be kept (still truthfully describes refresh token)")
	}
	for _, k := range []string{"cred", "success_count"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("key %q must survive the purge", k)
		}
	}

	// 2. Older note wording variant (0.1.50 doc header form): also purged.
	variant := []byte(`{"disabled":true,"note":"Session expired: re-login required"}`)
	if _, changed := stripLegacyDisabledFlag(variant); !changed {
		t.Fatal("Session-expired note prefix variant must be purged")
	}

	// 3. Manual-toggle shape (no note): NEVER touched — could be a
	//    legitimate manual disable; the purge cannot tell.
	manual := []byte(`{"disabled":true,"note":""}`)
	out3, changed3 := stripLegacyDisabledFlag(manual)
	if changed3 {
		t.Fatal("disabled without Session-expired note must report unchanged")
	}
	if string(out3) != string(manual) {
		t.Fatal("manual-shape doc must be returned as-is")
	}

	// 4. Disabled with an unrelated note: untouched.
	other := []byte(`{"disabled":true,"note":"imported via panel"}`)
	if _, changed := stripLegacyDisabledFlag(other); changed {
		t.Fatal("disabled with unrelated note must report unchanged")
	}

	// 5. Not disabled: untouched.
	clean := []byte(`{"disabled":false,"note":"Session expired (refresh token dead): re-login required"}`)
	if _, changed := stripLegacyDisabledFlag(clean); changed {
		t.Fatal("non-disabled doc must report unchanged (note alone is fine)")
	}

	// 6. Unparsable input: returned unchanged, never blind-written.
	bad := []byte(`{not json`)
	out6, changed6 := stripLegacyDisabledFlag(bad)
	if changed6 {
		t.Fatal("unparsable input must report unchanged")
	}
	if string(out6) != string(bad) {
		t.Fatal("unparsable input must be returned as-is")
	}
}
