// active_auth.go tracks the panel-selected TraeWork account used for routing.
// The selection is sticky; when the active account becomes exhausted /
// disabled / cooling down / anomalous / missing, routing switches to the next
// healthy candidate and remembers the choice.
package main

import (
	"strings"
	"sync"
)

var (
	activeAuthID string
	activeAuthMu sync.RWMutex
)

func getActiveAuthID() string {
	activeAuthMu.RLock()
	defer activeAuthMu.RUnlock()
	return strings.TrimSpace(activeAuthID)
}

func setActiveAuthID(id string) {
	id = strings.TrimSpace(id)
	activeAuthMu.Lock()
	activeAuthID = id
	activeAuthMu.Unlock()
}

// clearActiveAuthIfMatch clears the selection when the given auth is removed.
func clearActiveAuthIfMatch(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	activeAuthMu.Lock()
	if activeAuthID == id {
		activeAuthID = ""
	}
	activeAuthMu.Unlock()
}

// activeAuthCandidate is a thin view used by pickActiveAuth.
type activeAuthCandidate struct {
	ID        string
	Disabled  bool
	Exhausted bool
}

// pickActiveAuth chooses which traework auth to use from host candidates.
func pickActiveAuth(candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	byID := make(map[string]activeAuthCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	cur := getActiveAuthID()
	if cur != "" {
		if c, ok := byID[cur]; ok && !c.Disabled && !c.Exhausted && !isAccountCoolingDown(cur) && !isAccountAnomaly(cur) {
			return cur
		}
	}

	var next string
	for _, c := range candidates {
		if !c.Disabled && !c.Exhausted && !isAccountCoolingDown(c.ID) && !isAccountAnomaly(c.ID) {
			next = c.ID
			break
		}
	}
	if next == "" {
		if cur != "" {
			if _, ok := byID[cur]; ok {
				return cur
			}
		}
		next = candidates[0].ID
	}
	if next != "" && next != cur {
		setActiveAuthID(next)
	}
	return next
}

// traeAccountView is the dashboard's per-account card model.
type traeAccountView struct {
	AuthID        string `json:"auth_id"`
	AuthIndex     string `json:"auth_index"`
	Nickname      string `json:"nickname"`
	UID           string `json:"uid"`
	Remain        int64  `json:"remain"`
	Exhausted     bool   `json:"exhausted"`
	Disabled      bool   `json:"disabled"`
	Anomaly       bool   `json:"anomaly"`
	CoolingDown   bool   `json:"cooling_down"`
	FailCount     int    `json:"fail_count"`
	CooldownUntil string `json:"cooldown_until,omitempty"`
	Active        bool   `json:"active"`
}

// ensureDefaultActiveAuth keeps the panel selection consistent with routing
// (same rules as pickActiveAuth).
func ensureDefaultActiveAuth(accounts []traeAccountView) string {
	cur := getActiveAuthID()
	live := make(map[string]traeAccountView, len(accounts))
	for _, a := range accounts {
		live[a.AuthID] = a
	}
	if cur != "" {
		if a, ok := live[cur]; ok && !a.Disabled && !a.Exhausted && !a.CoolingDown && !a.Anomaly {
			return cur
		}
	}
	var firstAny, firstOK, firstReady string
	for _, a := range accounts {
		if firstAny == "" {
			firstAny = a.AuthID
		}
		if a.Disabled {
			continue
		}
		if firstOK == "" {
			firstOK = a.AuthID
		}
		if !a.Exhausted && !a.CoolingDown && !a.Anomaly && firstReady == "" {
			firstReady = a.AuthID
		}
	}
	next := firstReady
	if next == "" {
		if cur != "" {
			if a, ok := live[cur]; ok && !a.Disabled {
				return cur
			}
		}
		next = firstOK
	}
	if next == "" {
		next = firstAny
	}
	if next != "" && next != cur {
		setActiveAuthID(next)
	}
	return next
}
