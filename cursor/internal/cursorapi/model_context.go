package cursorapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type contextModel struct {
	Name               string   `json:"name"`
	ServerModelName    string   `json:"serverModelName"`
	LegacySlugs        []string `json:"legacySlugs"`
	IDAliases          []string `json:"idAliases"`
	ContextTokenLimit  int32    `json:"contextTokenLimit"`
	SupportsNonMaxMode *bool    `json:"supportsNonMaxMode"`
	Variants           []struct {
		LegacySlug string `json:"legacySlug"`
		IsMaxMode  bool   `json:"isMaxMode"`
	} `json:"variants"`
}

// ModelContextLengths describes the non-MAX execution mode used by this client.
func (client *Client) ModelContextLengths(ctx context.Context, accessToken string) (map[string]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body := strings.NewReader(`{"useModelParameters":true,"useReactModelPicker":true,"excludeMaxNamedModels":true}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint("/aiserver.v1.AiService/AvailableModels"), body)
	if err != nil {
		return nil, fmt.Errorf("create Cursor context metadata request: %w", err)
	}
	client.applyHeaders(request, requestHeaders{accessToken: accessToken, sessionID: randomUUID()}, "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request Cursor context metadata: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxModelResponseBytes+1))
	if err := errors.Join(readErr, response.Body.Close()); err != nil {
		return nil, fmt.Errorf("read Cursor context metadata: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cursor context metadata returned HTTP %d", response.StatusCode)
	}
	if len(raw) > maxModelResponseBytes {
		return nil, errors.New("Cursor context metadata exceeds 4 MiB")
	}
	var catalog struct {
		Models []contextModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("decode Cursor context metadata: %w", err)
	}
	limits := make(map[string]int64)
	for _, model := range catalog.Models {
		if model.ContextTokenLimit <= 0 || (model.SupportsNonMaxMode != nil && !*model.SupportsNonMaxMode) {
			continue
		}
		ids := append([]string{model.Name, model.ServerModelName}, model.LegacySlugs...)
		ids = append(ids, model.IDAliases...)
		for _, variant := range model.Variants {
			if !variant.IsMaxMode {
				ids = append(ids, variant.LegacySlug)
			}
		}
		for _, id := range ids {
			if id != "" {
				limits[id] = int64(model.ContextTokenLimit)
			}
		}
	}
	return limits, nil
}
