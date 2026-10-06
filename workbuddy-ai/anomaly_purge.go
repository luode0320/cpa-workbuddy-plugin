// anomaly_purge.go removes the legacy top-level nomaly flag from physical
// auth files.
package main

import (
	"encoding/json"
)

type authFileErr struct{ msg string }

func (e *authFileErr) Error() string { return e.msg }

func errAuthIndexRequired() error { return &authFileErr{msg: "auth_index is required"} }
func errAuthMissing() error       { return &authFileErr{msg: "auth file missing or empty"} }

func stripAnomalyKey(raw []byte) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return raw, false
	}
	if _, ok := doc["anomaly"]; !ok {
		return raw, false
	}
	delete(doc, "anomaly")
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}
	return out, true
}

func purgeLegacyAnomalyFlags() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	for _, f := range files {
		phys, err := hostAuthGetPhysical(f.AuthIndex)
		if err != nil || phys == nil || len(phys.JSON) == 0 {
			continue
		}
		clean, changed := stripAnomalyKey(phys.JSON)
		if !changed {
			continue
		}
		sa, _ := parseStored(clean)
		name := authFileNameFor(sa)
		path := ""
		legacyPath := ""
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
		_ = persistAuthDirect(name, path, legacyPath, clean)
	}
}
