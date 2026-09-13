// scheduler.go implements the CPA scheduler.pick capability for traework.
//
// Routing uses the panel-selected active account; when the selection is
// exhausted/disabled/missing/cooling-down, it switches to another
// healthy candidate. Non-traework candidates are always deferred so the
// built-in scheduler handles them, and when EVERY traework candidate is
// exhausted/cooling-down the pick is deferred as well (Handled: false) so
// the host can fail over to other providers' accounts (e.g. workbuddy).
// Only active when scheduler_mode: credits or session is configured
// (default off).
package main

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	schedulerMode   = schedulerModeOff
	schedulerModeMu sync.RWMutex
)

// setSchedulerMode is a test helper that returns a restore func.
func setSchedulerMode(mode string) func() {
	schedulerModeMu.Lock()
	old := schedulerMode
	schedulerMode = mode
	schedulerModeMu.Unlock()
	return func() {
		schedulerModeMu.Lock()
		schedulerMode = old
		schedulerModeMu.Unlock()
	}
}

func loadedSchedulerMode() string {
	schedulerModeMu.RLock()
	defer schedulerModeMu.RUnlock()
	return schedulerMode
}

// handleSchedulerPick selects a traework auth candidate based on the
// panel-selected active account. Non-traework candidates are always deferred
// (Handled: false) so the built-in scheduler handles them. When every
// traework candidate is exhausted or cooling down, the pick is deferred too
// (Handled: false) so the built-in scheduler can fail over to other
// providers' accounts — the plugin never answers with a dead candidate.
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	if loadedSchedulerMode() != schedulerModeCredits && loadedSchedulerMode() != schedulerModeSession {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// Collect traework candidates only. Cooldown filter applies last; when
	// EVERY candidate is filtered, keep the full list so the picker falls
	// back to the current pin (mirrors the all-exhausted fallback).
	var wbCandidates []pluginapi.SchedulerAuthCandidate
	for _, c := range req.Candidates {
		if c.Provider != providerName {
			continue
		}
		if candidateDisabled(c) {
			continue
		}
		wbCandidates = append(wbCandidates, c)
	}
	if len(wbCandidates) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	// Preserve filter: accounts the watchdog flagged (credits below
	// preserve_threshold) are kept out of routing entirely so they keep a
	// small credit buffer. Place this BEFORE the cooldown filter so the
	// lastNonEmpty fallback can still see preserved accounts when every
	// traework account is preserved — we don't want a fleet-wide credit
	// reset to lock routing.
	preserveFiltered := make([]pluginapi.SchedulerAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		if !isAccountPreserved(c.ID) {
			preserveFiltered = append(preserveFiltered, c)
		}
	}
	if len(preserveFiltered) > 0 {
		wbCandidates = preserveFiltered
	}
	filtered := make([]pluginapi.SchedulerAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		if !isAccountCoolingDown(c.ID) {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		// Every traework account is in failover cooldown: no healthy account
		// exists on this provider. Defer to the host's built-in scheduler so
		// it can fail over to OTHER providers' accounts (e.g. workbuddy)
		// instead of pinning the request to a dead account. Returning the
		// cooling accounts here (the old keep-full-list fallback) made the
		// plugin answer with Handled:true and a doomed candidate, which
		// blocked cross-provider failover until the request-level retry
		// budget was exhausted.
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	wbCandidates = filtered

	cands := make([]activeAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		_, exhausted := cachedCreditsScore(c.ID)
		cands = append(cands, activeAuthCandidate{
			ID:        c.ID,
			Disabled:  false, // already filtered
			Exhausted: exhausted,
		})
	}
	var picked string
	if loadedSchedulerMode() == schedulerModeSession {
		picked = pickSessionAuth(extractSessionKey(req), cands)
	} else {
		picked = pickActiveAuth(cands)
	}
	if picked == "" {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	return okEnvelope(pluginapi.SchedulerPickResponse{
		AuthID:  picked,
		Handled: true,
	})
}

// candidateDisabled reports host-disabled auth from Status/metadata.
func candidateDisabled(c pluginapi.SchedulerAuthCandidate) bool {
	st := strings.ToLower(strings.TrimSpace(c.Status))
	if st == "disabled" {
		return true
	}
	if c.Metadata != nil {
		if v, ok := c.Metadata["disabled"]; ok {
			switch t := v.(type) {
			case bool:
				return t
			case string:
				return strings.EqualFold(strings.TrimSpace(t), "true")
			}
		}
	}
	return false
}

// isAccountPreserved reports whether the account is currently flagged by the
// preserve watchdog and must be kept out of routing. Symmetric with
// isAccountCoolingDown: every scheduler pick asks the same predicate family.
func isAccountPreserved(authID string) bool {
	return isPreserve(authID)
}
