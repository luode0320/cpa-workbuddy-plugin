package main

import "time"

// decision is one routing choice, kept in a ring buffer for the UI.
type decision struct {
	Time time.Time `json:"time"`
	// Kind is "group" (model group fallback chain).
	Kind    string `json:"kind"`
	Model   string `json:"model"`
	Session string `json:"session,omitempty"`
	// AuthID is the model that served the group request.
	AuthID   string         `json:"auth_id,omitempty"`
	Reason   string         `json:"reason"`
	Attempts []groupAttempt `json:"attempts,omitempty"`
}

// sessionBinding pins a client session to a model of a group until it goes idle.
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
	Version   string           `json:"version"`
	Now       time.Time        `json:"now"`
	Config    pluginConfig     `json:"config"`
	Sessions  []sessionBinding `json:"sessions"`
	Decisions []decision       `json:"decisions"`
	// Models lists every model ID referenced by the configured groups, for the group editor.
	Models []string `json:"models"`
}
