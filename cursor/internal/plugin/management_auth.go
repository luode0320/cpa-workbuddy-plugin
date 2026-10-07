package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

const quotaUnavailableReason = "Cursor does not publish a subscription remaining-quota API"

func (handler *Handler) managementStatus(ctx context.Context) (managementResponse, error) {
	files, err := handler.cursorAuthFiles(ctx)
	if err != nil {
		return managementError(http.StatusBadGateway, err.Error()), nil
	}
	status := cursorManagementStatus{Provider: providerName, GeneratedAt: time.Now().UTC(), Accounts: make([]cursorAccountStatus, 0, len(files)), CheckpointMetrics: handler.usage.checkpoints.total(), ContextPolicy: currentContextPolicy()}
	type authRecord struct {
		file          hostAuthFile
		credential    cursorauth.Credentials
		credentialErr error
	}
	records := make([]authRecord, len(files))
	parents := make([]int, len(files))
	identityOwner := make(map[string]int, len(files))
	findRoot := func(index int) int {
		root := index
		for parents[root] != root {
			root = parents[root]
		}
		for parents[index] != index {
			next := parents[index]
			parents[index] = root
			index = next
		}
		return root
	}
	union := func(left, right int) {
		leftRoot := findRoot(left)
		rightRoot := findRoot(right)
		if leftRoot == rightRoot {
			return
		}
		if leftRoot < rightRoot {
			parents[rightRoot] = leftRoot
			return
		}
		parents[leftRoot] = rightRoot
	}
	for index, file := range files {
		parents[index] = index
		credential, credentialErr := handler.getCursorCredential(ctx, file.AuthIndex)
		identities := []string(nil)
		if credentialErr == nil {
			identities = cursorCredentialIdentities(credential)
			for _, identity := range identities {
				if owner, exists := identityOwner[identity]; exists {
					union(index, owner)
				} else {
					identityOwner[identity] = index
				}
			}
		}
		records[index] = authRecord{file: file, credential: credential, credentialErr: credentialErr}
	}
	accountByRoot := make(map[int]int, len(files))
	for index, record := range records {
		root := findRoot(index)
		if accountIndex, exists := accountByRoot[root]; exists {
			mergeCursorAccountMetrics(&status.Accounts[accountIndex], record.file, handler.usage)
			continue
		}
		account, accountErr := handler.cursorAccountStatusWithCredential(ctx, record.file, record.credential, record.credentialErr)
		if accountErr != nil {
			metricKey := cursorMetricKey(record.file)
			account = cursorAccountStatus{
				AuthIndex:          record.file.AuthIndex,
				Name:               record.file.Name,
				Label:              record.file.Label,
				Status:             "unavailable: " + accountErr.Error(),
				HostRuntime:        cursorHostRuntime(record.file),
				SubscriptionQuota:  cursorQuotaStatus{Status: "unavailable", Reason: quotaUnavailableReason},
				LocalUsage:         handler.usage.snapshot(metricKey),
				CheckpointMetrics:  handler.usage.checkpoints.snapshot(metricKey),
				Models:             []cursorModelStatus{},
				ModelContextStatus: "unavailable",
			}
		}
		accountByRoot[root] = len(status.Accounts)
		status.Accounts = append(status.Accounts, account)
	}
	return managementJSON(http.StatusOK, status)
}

func mergeCursorAccountMetrics(account *cursorAccountStatus, file hostAuthFile, usage *usageStore) {
	metricKey := cursorMetricKey(file)
	account.LocalUsage = mergeLocalUsage(account.LocalUsage, usage.snapshot(metricKey))
	account.CheckpointMetrics = mergeCheckpointMetricStatus(account.CheckpointMetrics, usage.checkpoints.snapshot(metricKey))
	account.HostRuntime.SuccessAttempts += file.Success
	account.HostRuntime.FailedAttempts += file.Failed
	if !strings.HasPrefix(account.Status, "unavailable:") {
		account.Status = cursorPluginStatus(account.HostRuntime.Status, account.LocalUsage.LastOutcome)
	}
}

func (handler *Handler) cursorAccountStatusWithCredential(ctx context.Context, file hostAuthFile, credential cursorauth.Credentials, credentialErr error) (cursorAccountStatus, error) {
	if credentialErr != nil {
		return cursorAccountStatus{}, credentialErr
	}
	models, err := handler.cursor.DiscoverModels(ctx, credential.AccessToken)
	if err != nil {
		return cursorAccountStatus{}, fmt.Errorf("discover Cursor models: %w", err)
	}
	items, metadataStatus := handler.managementModelContexts(ctx, models, credential)
	metricKey := cursorMetricKey(file)
	localUsage := handler.usage.snapshot(metricKey)
	return cursorAccountStatus{
		AuthIndex:          file.AuthIndex,
		Name:               file.Name,
		Label:              file.Label,
		Status:             cursorPluginStatus(file.Status, localUsage.LastOutcome),
		HostRuntime:        cursorHostRuntime(file),
		SubscriptionQuota:  cursorQuotaStatus{Status: "unavailable", Reason: quotaUnavailableReason},
		LocalUsage:         localUsage,
		CheckpointMetrics:  handler.usage.checkpoints.snapshot(metricKey),
		Models:             items,
		ModelContextStatus: metadataStatus,
	}, nil
}

func cursorMetricKey(file hostAuthFile) string {
	if id := strings.TrimSpace(file.ID); id != "" {
		return id
	}
	return file.AuthIndex
}

func cursorHostRuntime(file hostAuthFile) hostRuntimeStatus {
	return hostRuntimeStatus{
		Scope: "cli_proxy_process", Status: file.Status, StatusMessage: file.StatusMessage,
		SuccessAttempts: file.Success, FailedAttempts: file.Failed,
	}
}

func cursorPluginStatus(hostStatus, lastOutcome string) string {
	switch strings.ToLower(strings.TrimSpace(hostStatus)) {
	case "disabled", "inactive":
		return strings.ToLower(strings.TrimSpace(hostStatus))
	}
	if lastOutcome != "" && lastOutcome != "succeeded" {
		return "error"
	}
	return "active"
}

func cursorCredentialIdentities(credential cursorauth.Credentials) []string {
	identities := make([]string, 0, 2)
	if accountID := strings.ToLower(strings.TrimSpace(credential.AccountID)); accountID != "" {
		identities = append(identities, "account:"+accountID)
	}
	if email := strings.ToLower(strings.TrimSpace(credential.Email)); email != "" {
		identities = append(identities, "email:"+email)
	}
	return identities
}

func (handler *Handler) updateDisabledModels(ctx context.Context, body []byte) (managementResponse, error) {
	var update disabledModelsUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		return managementError(http.StatusBadRequest, "invalid JSON body"), nil
	}
	update.AuthIndex = strings.TrimSpace(update.AuthIndex)
	if update.AuthIndex == "" {
		return managementError(http.StatusBadRequest, "auth_index is required"), nil
	}
	get, credential, err := handler.getCursorAuth(ctx, update.AuthIndex)
	if err != nil {
		return managementError(http.StatusBadGateway, err.Error()), nil
	}
	available, err := handler.cursor.DiscoverModels(ctx, credential.AccessToken)
	if err != nil {
		return managementError(http.StatusBadGateway, "discover Cursor models: "+err.Error()), nil
	}
	known := normalizedModelSet(available)
	disabled := sortedUniqueModels(update.DisabledModels)
	for _, id := range disabled {
		if _, ok := known[id]; !ok {
			return managementError(http.StatusBadRequest, "unknown Cursor model: "+id), nil
		}
	}
	updatedJSON, err := setDisabledModels(get.JSON, disabled)
	if err != nil {
		return managementError(http.StatusInternalServerError, err.Error()), nil
	}
	if _, err := handler.host.Call(ctx, "host.auth.save", struct {
		Name string          `json:"name"`
		JSON json.RawMessage `json:"json"`
	}{Name: get.Name, JSON: updatedJSON}); err != nil {
		return managementError(http.StatusBadGateway, "save Cursor auth: "+err.Error()), nil
	}
	return managementJSON(http.StatusOK, map[string]any{
		"auth_index":      update.AuthIndex,
		"disabled_models": disabled,
	})
}
