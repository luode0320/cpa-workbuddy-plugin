// panel.go serves the TraeWork web panel (account credits, manual check-in,
// points refresh, enable/disable, unfreeze, failover status, preserve pool,
// keepalive, lifecycle, credential import). It is a self-contained HTML page
// that talks to the management API routes registered in management.go.
package main

import (
	_ "embed"
	"bytes"
	"encoding/json"
)

//go:embed panel.html
var panelHTML []byte

// traeStoragePath is the canonical Windows host path to the Trae SOLO
// globalStorage directory shown in the panel UI. The panel's job is to teach
// the user where their credentials live, not to detect them at runtime — the
// plugin server may run in a Linux container, in which case APPDATA / HOME
// would point somewhere irrelevant. Keep this in sync with the storage layout
// expected by import.go (parseCredentialImport reads the `iCubeAuthInfo://
// icube.cloudide` key from storage.json in this directory).
const traeStoragePath = `C:\Users\luode\AppData\Roaming\TRAE SOLO CN\User\globalStorage`

// servePanel returns the panel HTML for a resource sub-path. Unknown sub-paths
// fall back to the main dashboard. The Trae SOLO credential path is the fixed
// Windows host path (UI hint, not server-detected) — the plugin server may run
// in a Linux container and cannot predict the user's local Trae SOLO install
// location, so the path is hard-coded for clarity rather than injected at
// serve time.
//
// The path is injected in two forms:
//   - __STORAGE_DIR_DISPLAY__ → raw path, rendered inside an HTML <code> hint
//     (backslashes are fine in HTML text nodes).
//   - __STORAGE_DIR_JSON__ → JSON-escaped string literal, assigned to the JS
//     constant TRAE_STORAGE_PATH. JSON escaping doubles the backslashes so the
//     JS string survives parsing (single-quoted C:\Users\... would drop every
//     backslash as an unknown escape sequence).
func servePanel(sub string) []byte {
	_ = sub
	dirJSON, _ := json.Marshal(traeStoragePath)
	out := bytes.ReplaceAll(panelHTML, []byte("__STORAGE_DIR_JSON__"), dirJSON)
	out = bytes.ReplaceAll(out, []byte("__STORAGE_DIR_DISPLAY__"), []byte(traeStoragePath))
	return out
}
