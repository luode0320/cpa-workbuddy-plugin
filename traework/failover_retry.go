// failover_retry.go selects an alternate account for in-flight failover.
//
// When the executor hits an account-level 4xx (401/403/404/405) or 429, the
// request itself is fine — only this account is broken. Rather than failing
// the user, the executor tries the same request on a different traework
// account. pickNextAuth returns the next usable (authID, traeAuth) pair in a
// stable fallback order, skipping the current failing account and any that
// are disabled / cooling down / failing-credits / anomalous.
//
// Unlike qoderwork (per-account COSY signature over body+URL), the Trae
// llm_utils_chat request body is account-independent — only the auth headers
// (Bearer token + device fingerprint) change. Retrying means rebuilding the
// request with the new account's headers via rebuildRequestWithTraeAuth.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// pickNextAuth returns the next traework account to retry on after
// currentAuthID failed. It is best-effort: any error reading the host's auth
// list, loading a candidate's bundle, or evaluating a credential's state
// results in ok=false so the caller falls back to surfacing the original
// error to the user.
func pickNextAuth(currentAuthID string) (nextAuthID string, nextSA *traeAuth, ok bool) {
	currentAuthID = strings.TrimSpace(currentAuthID)
	files, err := hostAuthList()
	if err != nil || len(files) == 0 {
		return "", nil, false
	}

	for _, f := range files {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			continue
		}
		if id == currentAuthID {
			continue
		}
		if f.Disabled {
			continue
		}
		if isAccountCoolingDown(id) {
			continue
		}
		if isAccountAnomaly(id) {
			continue
		}
		if strings.TrimSpace(f.AuthIndex) == "" {
			continue
		}

		sa, phys, loadErr := hostAuthGetBundle(f.AuthIndex)
		if loadErr != nil || sa == nil {
			_ = phys
			continue
		}

		// The credential must carry a usable access token; an empty token
		// would 401 identically and burn a budget slot.
		if !sa.hasToken() {
			continue
		}

		return id, sa, true
	}
	return "", nil, false
}

// rebuildRequestWithTraeAuth builds a fresh request against the same Trae
// llm_utils_chat endpoint with the same body, but authenticated for a
// different account. The caller keeps the original request untouched for its
// own retry bookkeeping; only the account-bound headers are rebuilt.
func rebuildRequestWithTraeAuth(sa *traeAuth, body []byte, apiHost string) (*http.Request, error) {
	if sa == nil {
		return nil, fmt.Errorf("rebuildRequestWithTraeAuth: nil traeAuth")
	}
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiHost+llmUtilsChatPath, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header = buildTraeAuthHeaders(sa)
	return req, nil
}

// readAllUpstreamErr drains the response body of a failed 4xx/5xx upstream
// call into a bounded byte slice so callers can pass it to
// noteAccountFailure / publishUsage without holding the body open. Empty
// bodies collapse to "" — callers already handle that.
func readAllUpstreamErr(r io.Reader) string {
	if r == nil {
		return ""
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return string(buf)
}

// parseUpstreamStatusFromErr extracts the HTTP status code from an error
// produced by the "upstream N: ..." error shape shared by the executor
// paths. Returns 0 for transport-level errors, parse failures, and nil.
func parseUpstreamStatusFromErr(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "upstream ") {
		return 0
	}
	rest := strings.TrimPrefix(msg, "upstream ")
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		return 0
	}
	n, perr := strconv.Atoi(strings.TrimSpace(rest[:idx]))
	if perr != nil {
		return 0
	}
	return n
}
