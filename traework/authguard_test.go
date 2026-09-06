package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestReapplyDisabledFlag pins the fold semantics: the guarded disabled flag
// and its note are re-applied onto the CURRENT physical content (which the
// host may have rewritten, rotating tokens), and every other key is
// preserved verbatim. Already-disabled and unparsable inputs are no-ops.
func TestReapplyDisabledFlag(t *testing.T) {
	entry := guardEntry{authID: "id-a", note: "Session expired (refresh token dead): re-login required", source: "session-dead"}

	// Host-rebuilt doc: disabled/note wiped, token rotated by the host.
	wiped := []byte(`{"type":"traework-provider","provider":"traework-provider","uid":"4104930657578889","nickname":"用户2808303731","token":"new-rotated-token","refreshToken":"r","deviceId":"d","machineId":"m"}`)
	got, changed := reapplyDisabledFlag(wiped, entry)
	if !changed {
		t.Fatal("wiped doc must be rewritten")
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("folded doc must be valid JSON: %v", err)
	}
	if m["disabled"] != true {
		t.Fatal("disabled must be re-applied")
	}
	if m["note"] != entry.note {
		t.Fatalf("note must be re-applied, got %v", m["note"])
	}
	// Every other key preserved verbatim.
	want := map[string]any{"type": "traework-provider", "provider": "traework-provider", "uid": "4104930657578889", "nickname": "用户2808303731", "token": "new-rotated-token", "refreshToken": "r", "deviceId": "d", "machineId": "m", "disabled": true, "note": entry.note}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("fold must preserve all host keys: got %v", m)
	}

	// Already disabled: no-op.
	still := []byte(`{"disabled":true,"note":"x","token":"t"}`)
	if _, changed := reapplyDisabledFlag(still, entry); changed {
		t.Fatal("already-disabled doc must be untouched")
	}

	// Unparsable: never blind-write.
	if _, changed := reapplyDisabledFlag([]byte("not-json"), entry); changed {
		t.Fatal("unparsable doc must be untouched")
	}
}
