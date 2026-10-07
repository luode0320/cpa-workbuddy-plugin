package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

type authModelRequest struct {
	StorageJSON []byte `json:"StorageJSON"`
}

type modelContextProvider interface {
	ModelContextLengths(context.Context, string) (map[string]int64, error)
}

func (handler *Handler) modelsForAuth(ctx context.Context, raw []byte) (any, error) {
	var request authModelRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("decode auth model request: %w", err)
	}
	credentials, err := cursorauth.ParseCredentials(request.StorageJSON)
	if err != nil {
		return nil, err
	}
	models, err := handler.cursor.DiscoverModels(ctx, credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	var contexts map[string]int64
	if provider, ok := handler.cursor.(modelContextProvider); ok {
		contexts, err = provider.ModelContextLengths(ctx, credentials.AccessToken)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			contexts = nil
		}
	}
	return modelResponse(filterDisabledModels(models, credentials.DisabledModels), contexts), nil
}

func filterDisabledModels(models, disabled []string) []string {
	if len(disabled) == 0 {
		return append([]string(nil), models...)
	}
	blocked := make(map[string]struct{}, len(disabled))
	for _, id := range disabled {
		normalized := strings.TrimPrefix(strings.TrimSpace(id), "cursor/")
		if normalized != "" {
			blocked[normalized] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(models))
	for _, id := range models {
		normalized := strings.TrimPrefix(strings.TrimSpace(id), "cursor/")
		if _, found := blocked[normalized]; !found && normalized != "" {
			filtered = append(filtered, normalized)
		}
	}
	return filtered
}

func modelResponse(ids []string, contexts map[string]int64) any {
	models := make([]modelInfo, 0, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		models = append(models, modelInfo{
			ID:                         "cursor/" + id,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                "Cursor " + id,
			SupportedGenerationMethods: []string{"chat"},
			SupportedInputModalities:   []string{"text", "image"},
			SupportedOutputModalities:  []string{"text", "image"},
			ContextLength:              effectiveContextLength(contexts[id]),
			NativeContextLength:        contexts[id],
			ClientContextLimit:         clientContextLimit,
			UserDefined:                true,
		})
	}
	return struct {
		Provider string      `json:"Provider"`
		Models   []modelInfo `json:"Models"`
	}{Provider: providerName, Models: models}
}
