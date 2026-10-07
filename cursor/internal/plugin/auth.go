package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
)

type authParseRequest struct {
	FileName string `json:"FileName"`
	RawJSON  []byte `json:"RawJSON"`
}

type authPollRequest struct {
	State string `json:"State"`
}

type authRefreshRequest struct {
	StorageJSON []byte `json:"StorageJSON"`
}

func (handler *Handler) parseAuth(raw []byte) (any, error) {
	var request authParseRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("decode auth parse request: %w", err)
	}
	credentials, err := cursorauth.ParseCredentials(request.RawJSON)
	if err != nil {
		return map[string]bool{"Handled": false}, nil
	}
	auth, err := buildAuthData(credentials)
	if err != nil {
		return nil, err
	}
	if request.FileName != "" {
		auth.FileName = request.FileName
	}
	auth.ID = ""
	return struct {
		Handled bool     `json:"Handled"`
		Auth    authData `json:"Auth"`
	}{Handled: true, Auth: auth}, nil
}

func (handler *Handler) startLogin(ctx context.Context) (any, error) {
	start, err := handler.auth.StartLogin(ctx)
	if err != nil {
		return nil, err
	}
	return struct {
		Provider  string    `json:"Provider"`
		URL       string    `json:"URL"`
		State     string    `json:"State"`
		ExpiresAt time.Time `json:"ExpiresAt"`
	}{Provider: providerName, URL: start.URL, State: start.State, ExpiresAt: start.ExpiresAt}, nil
}

func (handler *Handler) pollLogin(ctx context.Context, raw []byte) (any, error) {
	var request authPollRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("decode auth poll request: %w", err)
	}
	result, err := handler.auth.PollLogin(ctx, request.State)
	if err != nil {
		return nil, err
	}
	if result.Status == cursorauth.LoginPending {
		return map[string]string{"Status": "pending", "Message": "Waiting for Cursor login approval"}, nil
	}
	auth, err := buildAuthData(result.Credentials)
	if err != nil {
		return nil, err
	}
	return struct {
		Status  string   `json:"Status"`
		Message string   `json:"Message"`
		Auth    authData `json:"Auth"`
	}{Status: "success", Message: "Cursor login complete", Auth: auth}, nil
}

func (handler *Handler) refreshAuth(ctx context.Context, raw []byte) (any, error) {
	var request authRefreshRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("decode auth refresh request: %w", err)
	}
	current, err := cursorauth.ParseCredentials(request.StorageJSON)
	if err != nil {
		return nil, err
	}
	refreshed, err := handler.auth.Refresh(ctx, current)
	if err != nil {
		return nil, err
	}
	auth, err := buildAuthData(refreshed)
	if err != nil {
		return nil, err
	}
	auth.ID = ""
	auth.FileName = ""
	return struct {
		Auth             authData  `json:"Auth"`
		NextRefreshAfter time.Time `json:"NextRefreshAfter"`
	}{Auth: auth, NextRefreshAfter: refreshed.ExpiresAt}, nil
}

func buildAuthData(credentials cursorauth.Credentials) (authData, error) {
	storage, err := cursorauth.MarshalCredentials(credentials)
	if err != nil {
		return authData{}, err
	}
	identity := strings.TrimSpace(credentials.AccountID)
	if identity == "" {
		identity = strings.TrimSpace(credentials.Email)
	}
	if identity == "" {
		return authData{}, errors.New("Cursor account identity is unavailable")
	}
	digest := sha256.Sum256([]byte(identity))
	id := "cursor-" + hex.EncodeToString(digest[:8]) + ".json"
	label := credentials.Email
	if label == "" {
		label = "Cursor account"
	}
	return authData{
		Provider:         providerName,
		ID:               id,
		FileName:         id,
		Label:            label,
		StorageJSON:      storage,
		Metadata:         map[string]string{"type": providerName, "email": credentials.Email},
		Attributes:       map[string]string{"account_id": credentials.AccountID},
		NextRefreshAfter: credentials.ExpiresAt,
	}, nil
}
