// panel.go serves the TraeWork web panel (account credits, manual check-in,
// points refresh, enable/disable, unfreeze, failover status, preserve pool,
// keepalive, lifecycle, credential import). It is a self-contained HTML page
// that talks to the management API routes registered in management.go.
package main

import (
	_ "embed"
)

//go:embed panel.html
var panelHTML []byte

// servePanel returns the panel HTML for a resource sub-path. Unknown sub-paths
// fall back to the main dashboard. The panel is fully self-contained: the
// credential import hint no longer needs a server-injected storage directory
// (the user imports via the panel modal), so the embedded HTML is served
// verbatim.
func servePanel(sub string) []byte {
	_ = sub
	return panelHTML
}
