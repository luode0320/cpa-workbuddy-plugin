package plugin

import (
	"context"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

type cursorModelStatus struct {
	ID                  string `json:"id"`
	Disabled            bool   `json:"disabled"`
	NativeContextLength int64  `json:"native_context_length,omitempty"`
	ClientContextLimit  int64  `json:"client_context_limit"`
	ContextLength       int64  `json:"context_length,omitempty"`
}

type contextPolicyStatus struct {
	ClientContextLimit int64  `json:"client_context_limit"`
	CountMethod        string `json:"count_method"`
	Reserve            int64  `json:"reserve_units"`
	NativeMode         string `json:"native_mode"`
}

func currentContextPolicy() contextPolicyStatus {
	return contextPolicyStatus{ClientContextLimit: clientContextLimit, CountMethod: contextCountMethod, Reserve: contextReserve, NativeMode: "non_max"}
}

func (handler *Handler) managementModelContexts(ctx context.Context, models []string, credential cursorauth.Credentials) ([]cursorModelStatus, string) {
	var contexts map[string]int64
	metadataStatus := "unsupported"
	if provider, ok := handler.cursor.(modelContextProvider); ok {
		var err error
		contexts, err = provider.ModelContextLengths(ctx, credential.AccessToken)
		metadataStatus = "available"
		if err != nil {
			contexts = nil
			metadataStatus = "unavailable"
		}
	}
	disabled := normalizedModelSet(credential.DisabledModels)
	items := make([]cursorModelStatus, 0, len(models))
	for _, model := range models {
		id := normalizeModelID(model)
		if id == "" {
			continue
		}
		_, blocked := disabled[id]
		items = append(items, cursorModelStatus{
			ID: id, Disabled: blocked, NativeContextLength: max(contexts[id], 0),
			ClientContextLimit: clientContextLimit, ContextLength: effectiveContextLength(contexts[id]),
		})
	}
	return items, metadataStatus
}
