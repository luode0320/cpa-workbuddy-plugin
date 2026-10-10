// Package mimic generates ZCode client request fingerprints.
package mimic

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

type Config struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	AppVersion     string `yaml:"app_version" json:"app_version"`
	SessionID      string `yaml:"session_id" json:"session_id"`
	TraceID        string `yaml:"trace_id" json:"trace_id"`
	UserID         string `yaml:"user_id" json:"user_id"`
	Platform       string `yaml:"platform" json:"platform"`
	OSCategory     string `yaml:"os_category" json:"os_category"`
	OSVersion      string `yaml:"os_version" json:"os_version"`
	Language       string `yaml:"language" json:"language"`
	Timezone       string `yaml:"timezone" json:"timezone"`
	ReleaseChannel string `yaml:"release_channel" json:"release_channel"`
	Title          string `yaml:"title" json:"title"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:        true,
		AppVersion:     "3.14.4",
		Platform:       "win32-x64",
		OSCategory:     "windows",
		OSVersion:      "10.0.19045",
		Language:       "zh-CN",
		Timezone:       "Asia/Shanghai",
		ReleaseChannel: "production",
		Title:          "Z Code@electron",
	}
}

type Fingerprint struct {
	cfg       Config
	sessionID string
	traceID   string
	userID    string
}

func New(cfg Config, keyID string) *Fingerprint {
	return &Fingerprint{
		cfg:       cfg,
		sessionID: orUUID(cfg.SessionID),
		traceID:   orUUID(cfg.TraceID),
		userID:    stableUserID(cfg.UserID, keyID),
	}
}

func (f *Fingerprint) UserID() string {
	return f.userID
}

func (f *Fingerprint) ApplyRequest(h http.Header, apiKey, requestID string) {
	if !f.cfg.Enabled {
		h.Set("x-api-key", apiKey)
		return
	}
	h.Set("Content-Type", "application/json")
	h.Set("anthropic-version", "2023-06-01")
	h.Set("x-api-key", apiKey)
	h.Set("http-referer", "https://zcode.z.ai")
	h.Set("User-Agent", "ZCode/"+f.cfg.AppVersion)
	h.Set("x-zcode-app-version", f.cfg.AppVersion)
	h.Set("x-title", f.cfg.Title)
	h.Set("x-release-channel", f.cfg.ReleaseChannel)
	h.Set("x-client-language", f.cfg.Language)
	h.Set("x-client-timezone", f.cfg.Timezone)
	h.Set("x-zcode-agent", "glm")
	h.Set("x-platform", f.cfg.Platform)
	h.Set("x-os-category", f.cfg.OSCategory)
	h.Set("x-os-version", f.cfg.OSVersion)
	h.Set("x-request-id", requestID)
	h.Set("x-zcode-session-type", "main")
	h.Set("x-zcode-trace-id", f.traceID)
	h.Set("x-query-id", UUID7())
	h.Set("x-session-id", f.sessionID)
}

func orUUID(s string) string {
	if s != "" {
		return s
	}
	return UUID4()
}

func stableUserID(configured, keyID string) string {
	if configured != "" {
		return configured
	}
	h := sha1.New()
	h.Write([]byte("zcode2api:user"))
	h.Write([]byte(keyID))
	var b [16]byte
	copy(b[:], h.Sum(nil))
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func UUID4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func UUID7() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}
