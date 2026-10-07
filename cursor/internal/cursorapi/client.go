package cursorapi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

const (
	maxModelResponseBytes    = 4 << 20
	defaultHeartbeatInterval = 5 * time.Second
	defaultFirstDataTimeout  = 30 * time.Second
	defaultFrameSilence      = 30 * time.Second
	defaultProgressTimeout   = 90 * time.Second
	defaultOverallTimeout    = 15 * time.Minute
	defaultTrailingDrain     = 100 * time.Millisecond
	defaultClientVersion     = "cli-2026.07.16-899851b"
)

var (
	ErrFirstDataTimeout    = errors.New("Cursor transport timed out before first response data")
	ErrFirstFrameTimeout   = ErrFirstDataTimeout
	ErrFrameSilenceTimeout = errors.New("Cursor transport timed out between decoded frames")
	ErrProgressTimeout     = errors.New("Cursor transport timed out without meaningful progress")
)

type Config struct {
	BaseURL              string
	ClientVersion        string
	HTTPClient           *http.Client
	HeartbeatInterval    time.Duration
	FirstDataTimeout     time.Duration
	FirstFrameTimeout    time.Duration
	FrameSilenceTimeout  time.Duration
	ProgressTimeout      time.Duration
	OverallTimeout       time.Duration
	TrailingDrainTimeout time.Duration
}

type Client struct {
	baseURL       *url.URL
	clientVersion string
	httpClient    *http.Client
	workspacePath string
	projectFolder string
	heartbeat     time.Duration
	firstData     time.Duration
	frameSilence  time.Duration
	progress      time.Duration
	overall       time.Duration
	trailingDrain time.Duration
}

func NewClient(config Config) (*Client, error) {
	baseURL, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse Cursor base URL: %w", err)
	}
	if baseURL.Scheme != "https" || baseURL.Host == "" {
		return nil, errors.New("Cursor base URL must be HTTPS")
	}
	workspacePath, projectFolder, err := cursorWorkspaceContext()
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ForceAttemptHTTP2 = true
		client = &http.Client{Transport: transport}
	}
	if config.ClientVersion == "" {
		config.ClientVersion = defaultClientVersion
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.FirstDataTimeout <= 0 {
		config.FirstDataTimeout = config.FirstFrameTimeout
	}
	if config.FirstDataTimeout <= 0 {
		config.FirstDataTimeout = defaultFirstDataTimeout
	}
	if config.FrameSilenceTimeout <= 0 {
		config.FrameSilenceTimeout = defaultFrameSilence
	}
	if config.ProgressTimeout <= 0 {
		config.ProgressTimeout = defaultProgressTimeout
	}
	if config.OverallTimeout <= 0 {
		config.OverallTimeout = defaultOverallTimeout
	}
	if config.TrailingDrainTimeout <= 0 {
		config.TrailingDrainTimeout = defaultTrailingDrain
	}
	return &Client{
		baseURL:       baseURL,
		clientVersion: config.ClientVersion,
		httpClient:    client,
		workspacePath: workspacePath,
		projectFolder: projectFolder,
		heartbeat:     config.HeartbeatInterval,
		firstData:     config.FirstDataTimeout,
		frameSilence:  config.FrameSilenceTimeout,
		progress:      config.ProgressTimeout,
		overall:       config.OverallTimeout,
		trailingDrain: config.TrailingDrainTimeout,
	}, nil
}

func cursorWorkspaceContext() (string, string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("resolve Cursor workspace directory: %w", err)
	}
	homeDirectory, err := os.UserHomeDir()
	if err != nil || homeDirectory == "" {
		homeDirectory = workingDirectory
	}
	projectName := cursorProjectName(workingDirectory)
	return workingDirectory, filepath.Join(homeDirectory, ".cursor", "projects", projectName), nil
}

func cursorProjectName(workspacePath string) string {
	var name strings.Builder
	separator := false
	for _, character := range workspacePath {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			if separator && name.Len() > 0 {
				name.WriteByte('-')
			}
			name.WriteRune(character)
			separator = false
			continue
		}
		separator = true
	}
	return name.String()
}

func (client *Client) DiscoverModels(ctx context.Context, accessToken string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint("/agent.v1.AgentService/GetUsableModels"), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create Cursor model request: %w", err)
	}
	client.applyHeaders(request, requestHeaders{accessToken: accessToken, sessionID: randomUUID()}, "application/proto")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request Cursor models: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxModelResponseBytes+1))
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read Cursor models: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cursor models returned HTTP %d", response.StatusCode)
	}
	if len(raw) > maxModelResponseBytes {
		return nil, errors.New("Cursor models response exceeds 4 MiB")
	}
	models, err := cursorproto.DecodeModels(raw)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, errors.New("Cursor returned no usable models")
	}
	return models, nil
}

type requestHeaders struct {
	accessToken string
	requestID   string
	sessionID   string
}

func (client *Client) applyHeaders(request *http.Request, values requestHeaders, contentType string) {
	request.Header.Set("content-type", contentType)
	request.Header.Set("connect-protocol-version", "1")
	request.Header.Set("te", "trailers")
	request.Header.Set("authorization", "Bearer "+values.accessToken)
	request.Header.Set("x-ghost-mode", "true")
	request.Header.Set("x-cursor-client-version", client.clientVersion)
	request.Header.Set("x-cursor-client-type", "cli")
	request.Header.Set("x-session-id", values.sessionID)
	if values.requestID != "" {
		request.Header.Set("x-request-id", values.requestID)
	}
}

func (client *Client) endpoint(path string) string {
	return strings.TrimRight(client.baseURL.String(), "/") + path
}

func randomUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		now := uint64(time.Now().UnixNano())
		binary.BigEndian.PutUint64(value[:8], now)
		binary.BigEndian.PutUint64(value[8:], now^0xa5a5a5a5a5a5a5a5)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	)
}
