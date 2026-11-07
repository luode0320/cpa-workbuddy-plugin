package main

import "time"

// hostAuth is one credential from the host.auth.list callback (pluginapi.HostAuthFileEntry).
type hostAuth struct {
	ID             string    `json:"id"`
	AuthIndex      string    `json:"auth_index"`
	Name           string    `json:"name"`
	Provider       string    `json:"provider"`
	Label          string    `json:"label"`
	Email          string    `json:"email"`
	Status         string    `json:"status"`
	Disabled       bool      `json:"disabled"`
	Unavailable    bool      `json:"unavailable"`
	NextRetryAfter time.Time `json:"next_retry_after"`
}

// quotaWindow is one rate-limit window of a credential, normalized across providers.
type quotaWindow struct {
	// Name labels the window for display, e.g. "7d", "5h", "weekly", "Claude and GPT models: weekly".
	Name string `json:"name"`
	// Remaining is the unused fraction of the window, 0..1.
	Remaining float64 `json:"remaining"`
	// ResetAt is when the window refills; zero when the provider does not report it.
	ResetAt time.Time `json:"reset_at"`
	// Period is the window length (5h, 7d, 30d, ...); zero when unknown.
	Period time.Duration `json:"period"`
	// Models lists the model IDs the window applies to; empty means every model of the credential.
	Models []string `json:"models,omitempty"`
}

// credentialQuota is the latest quota snapshot of one credential.
type credentialQuota struct {
	AuthID    string        `json:"auth_id"`
	AuthIndex string        `json:"auth_index"`
	Provider  string        `json:"provider"`
	Source    string        `json:"source"`
	Plan      string        `json:"plan,omitempty"`
	Windows   []quotaWindow `json:"windows"`
	FetchedAt time.Time     `json:"fetched_at"`
	// Error is the last fetch failure; a credential with an error is not ranked.
	Error string `json:"error,omitempty"`
}

// rankEntry is one credential's evaluation for a spread decision.
type rankEntry struct {
	AuthID    string  `json:"auth_id"`
	Provider  string  `json:"provider"`
	Window    string  `json:"window,omitempty"`
	Remaining float64 `json:"remaining"`
	// HoursToReset is the time until the governing window resets.
	HoursToReset float64 `json:"hours_to_reset"`
	// Rate is remaining / hours_to_reset; higher wins.
	Rate float64 `json:"rate"`
	// Excluded explains why the credential was not eligible; empty when eligible.
	Excluded string `json:"excluded,omitempty"`
}

// decision is one routing or scheduling choice, kept in a ring buffer for the UI.
type decision struct {
	Time time.Time `json:"time"`
	// Kind is "balance" (credential pick for a model) or "group" (model group fallback).
	Kind    string `json:"kind"`
	Model   string `json:"model"`
	Session string `json:"session,omitempty"`
	// AuthID is the chosen credential for "balance" and the serving model for "group".
	AuthID   string         `json:"auth_id,omitempty"`
	Reason   string         `json:"reason"`
	Ranking  []rankEntry    `json:"ranking,omitempty"`
	Attempts []groupAttempt `json:"attempts,omitempty"`
}

// sessionBinding pins a client session to a credential until it goes idle.
type sessionBinding struct {
	Session  string    `json:"session"`
	AuthID   string    `json:"auth_id"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	BoundAt  time.Time `json:"bound_at"`
	LastSeen time.Time `json:"last_seen"`
	// IdleExpiresAt is LastSeen + idle_minutes; after it the next prompt re-picks.
	IdleExpiresAt time.Time `json:"idle_expires_at"`
}

// uiState is the JSON served to the management page.
type uiState struct {
	Version     string           `json:"version"`
	Now         time.Time        `json:"now"`
	Config      pluginConfig     `json:"config"`
	Credentials []uiCredential   `json:"credentials"`
	Sessions    []sessionBinding `json:"sessions"`
	Decisions   []decision       `json:"decisions"`
	// Models lists every model ID served by a credential, for the group editor.
	Models []string `json:"models"`
	// Error is set when the host credential list could not be read.
	Error string `json:"error,omitempty"`
	// ManagementError explains why quota collection is not running (no key, rejected key, IP ban).
	ManagementError string `json:"management_error,omitempty"`
}

// uiCredential joins a credential with its models and quota.
type uiCredential struct {
	Auth  hostAuth        `json:"auth"`
	Quota credentialQuota `json:"quota"`
	// HasQuota reports whether Quota holds a fetched snapshot.
	HasQuota bool     `json:"has_quota"`
	Models   []string `json:"models"`
}
