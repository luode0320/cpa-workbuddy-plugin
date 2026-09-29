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
//
// Only "available" accounts participate: 「测试」标签 (test_failed) and 冷却
// (failover cooldown) are hard-excluded — they are unusable by product
// definition, so there is no fallback that re-admits them. Survivors are
// ordered LOW-CREDIT-FIRST so the soonest-to-exhaust accounts burn down first.
package main

import (
	"encoding/json"
	"sort"
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
	// Hard exclusion: 「测试」标签 (scheduled active ping failed while credits
	// remained) / 冷却 (failover cooldown) accounts never carry traffic. Both
	// are "not available" by product definition, so they are filtered
	// unconditionally — there is NO keep-full-list fallback: when every
	// candidate is excluded we defer (Handled: false) so the host's built-in
	// scheduler can fail over to OTHER providers' accounts (e.g. workbuddy)
	// instead of pinning the request to an account we just declared unusable.
	available := make([]pluginapi.SchedulerAuthCandidate, 0, len(wbCandidates))
	for _, c := range wbCandidates {
		if isTestFailed(c.ID) || isAccountCoolingDown(c.ID) {
			continue
		}
		available = append(available, c)
	}
	if len(available) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	wbCandidates = available

	// Low-credit first: burn the soonest-to-exhaust accounts first, so the
	// fleet's remaining balance is concentrated on fewer accounts instead of
	// spread thin everywhere. Unknown credits (-1, no cached snapshot yet) go
	// LAST — never let an unmeasured account jump ahead of a measured one.
	// Candidate order is what session routing and the panel fallback use.
	sort.SliceStable(wbCandidates, func(i, j int) bool {
		left, _ := cachedCreditsScore(wbCandidates[i].ID)
		right, _ := cachedCreditsScore(wbCandidates[j].ID)
		if (left < 0) != (right < 0) {
			return right < 0
		}
		return left < right
	})

	// Build thin view for active-auth picker. Exhausted accounts are passed
	// through with Exhausted=true (rather than pre-filtered) so the picker can
	// skip them and still report "no healthy account" when they are all spent.
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
