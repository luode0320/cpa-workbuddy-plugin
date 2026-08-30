// credential.go owns the Trae credential model and the tc-header decryption
// (a Go port of trae-solo-local-api/src/trae-decrypt.js: AES-128-CBC with a
// sha512 integrity check over the plaintext). The plugin stores the raw
// credential string the user pastes (base64 tc-header blob or plaintext JSON
// from storage.json) and decrypts it at load time; only the decrypted fields
// are used for upstream auth.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	headerSize   = 6
	randomBytes  = 32
	hashSize     = 64
	aesKeySize   = 16
	ivSize       = 16
	authKey      = "iCubeAuthInfo://icube.cloudide"
	maxValueSize = 64 * 1024
)

// Salts are hard-coded constants from the Trae desktop client (JS port).
var (
	saltA = []byte{82, 9, 106, 213, 48, 54, 165, 56, 191, 64, 163, 158, 129, 243, 215, 251, 124, 227, 57, 130, 155, 47, 255, 135, 52, 142, 67, 68, 196, 222, 233, 203, 84, 123, 148, 50, 166, 194, 35, 61, 238, 76, 149, 11, 66, 250, 195, 78, 8, 46, 161, 102, 40, 217, 36, 178, 118, 91, 162, 73, 109, 139, 209, 37}
	saltB = []byte{31, 221, 168, 51, 136, 7, 199, 49, 177, 18, 16, 89, 39, 128, 236, 95, 96, 81, 127, 169, 25, 181, 74, 13, 45, 229, 122, 159, 147, 201, 156, 239, 160, 224, 59, 77, 174, 42, 245, 176, 200, 235, 187, 60, 131, 83, 153, 97, 23, 43, 4, 126, 186, 119, 214, 38, 225, 105, 20, 99, 85, 33, 12, 125}
	saltC = []byte{191, 192, 216, 250, 122, 246, 220, 97, 31, 254, 98, 27, 8, 72, 71, 176, 135, 99, 96, 18, 127, 101, 203, 104, 211, 102, 191, 125, 37, 72, 150, 156, 51, 229, 121, 35, 17, 153, 141, 177, 110, 131, 150, 128, 172, 255, 254, 6, 18, 140, 55, 62, 236, 249, 135, 64, 135, 12, 117, 4, 89, 149, 168, 209}
	saltD = []byte{246, 204, 26, 232, 232, 70, 129, 109, 223, 146, 169, 242, 23, 241, 105, 145, 50, 196, 165, 42, 254, 120, 3, 54, 244, 207, 209, 85, 53, 6, 138, 106, 175, 148, 31, 204, 186, 186, 165, 182, 87, 142, 49, 10, 39, 110, 26, 154, 86, 56, 173, 125, 18, 64, 198, 225, 99, 99, 83, 82, 191, 134, 76, 170}
)

// traeCredential is the decrypted Trae auth payload stored under the icube
// key. userRegion and account are JSON objects in the real payload; keep them
// raw so the structure survives Trae-side field additions.
type traeCredential struct {
	Token            string          `json:"token"`
	RefreshToken     string          `json:"refreshToken"`
	ExpiredAt        string          `json:"expiredAt"`
	RefreshExpiredAt string          `json:"refreshExpiredAt"`
	TokenReleaseAt   string          `json:"tokenReleaseAt"`
	UserID           string          `json:"userId"`
	Host             string          `json:"host"`
	UserRegion       json.RawMessage `json:"userRegion"`
	Account          json.RawMessage `json:"account"`
}

// accountName returns username, falling back to email, from the account
// object; "" when absent.
func (c *traeCredential) accountName() string {
	if len(c.Account) == 0 {
		return ""
	}
	var info struct {
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	if err := json.Unmarshal(c.Account, &info); err != nil {
		return ""
	}
	if info.Username != "" {
		return info.Username
	}
	return info.Email
}

// traeAuth is the plugin-side credential stored in the host auth file.
//
//	credential: raw value pasted from storage.json (tc-header base64 or
//	            plaintext JSON) — persisted, never logged.
//	token/refreshToken/uid/nickname/host: decrypted at load time (runtime).
//	deviceId/machineId: optional client fingerprint (check-in de-dupe).
type traeAuth struct {
	CredentialRaw    string `json:"credential,omitempty"`
	Token            string `json:"token,omitempty"`
	RefreshToken     string `json:"refreshToken,omitempty"`
	ExpiredAt        string `json:"expiredAt,omitempty"`
	RefreshExpiredAt string `json:"refreshExpiredAt,omitempty"`
	UserID           string `json:"uid,omitempty"`
	Nickname         string `json:"nickname,omitempty"`
	Host             string `json:"host,omitempty"`
	DeviceID         string `json:"deviceId,omitempty"`
	MachineID        string `json:"machineId,omitempty"`
}

// hasToken reports whether the auth carries a usable access token.
func (a *traeAuth) hasToken() bool {
	return a != nil && trimSpace(a.Token) != ""
}

func xorSalts(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func detectEncType(header []byte) string {
	if header[0] == 0x74 && header[1] == 0x63 && header[2] == 0x05 && header[3] == 0x10 && header[4] == 0x00 && header[5] == 0x00 {
		return "AES"
	}
	if header[0] == 18 && header[1] == 57 && header[2] == 32 && header[3] == 32 && header[4] == 2 && header[5] == 3 {
		return "AES_PRIVATE"
	}
	return "UNKNOWN"
}

func deriveKeyAndIV(random, salt []byte) ([]byte, []byte) {
	h := sha512.Sum512(random)
	combined := append(append([]byte{}, h[:]...), salt...)
	final := sha512.Sum512(combined)
	return final[:aesKeySize], final[aesKeySize : aesKeySize+ivSize]
}

func aesCBCDecrypt(key, iv, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext not block-aligned")
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	out := make([]byte, len(data))
	mode.CryptBlocks(out, data)
	// Go's crypto/cipher does NOT strip PKCS#7 padding (Node's
	// decipher.final() does); remove it so the sha512 check runs over the
	// exact plaintext bytes.
	return removePKCS7Padding(out)
}

func removePKCS7Padding(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty plaintext")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(data) {
		return nil, errors.New("invalid pkcs7 padding")
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, errors.New("invalid pkcs7 padding")
		}
	}
	return data[:len(data)-padLen], nil
}

func decryptTCBuffer(enc []byte) ([]byte, error) {
	if len(enc) < headerSize+randomBytes+hashSize+16 {
		return nil, fmt.Errorf("buffer too short: %d", len(enc))
	}
	encType := detectEncType(enc[:headerSize])
	if encType == "UNKNOWN" {
		return nil, fmt.Errorf("unknown encryption type: header %x", enc[:headerSize])
	}
	var salt []byte
	if encType == "AES_PRIVATE" {
		salt = xorSalts(saltC, saltD)
	} else {
		salt = xorSalts(saltA, saltB)
	}
	random := enc[headerSize : headerSize+randomBytes]
	encrypted := enc[headerSize+randomBytes:]
	key, iv := deriveKeyAndIV(random, salt)
	decrypted, err := aesCBCDecrypt(key, iv, encrypted)
	if err != nil {
		return nil, fmt.Errorf("aes decrypt: %w", err)
	}
	if len(decrypted) < hashSize {
		return nil, errors.New("decrypted data too short")
	}
	stored := decrypted[:hashSize]
	plain := decrypted[hashSize:]
	computed := sha512.Sum512(plain)
	if !equalBytes(stored, computed[:]) {
		return nil, errors.New("hash verification failed")
	}
	return plain, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// decryptCredentialString turns the pasted storage.json value into the
// decrypted credential. Accepts both the base64 tc-header blob and plaintext
// JSON (some editions store it unencrypted).
func decryptCredentialString(raw string) (*traeCredential, error) {
	trimmed := trimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxValueSize {
		return nil, errors.New("credential empty or oversized")
	}
	var cred traeCredential
	if trimmed[0] == '{' || trimmed[0] == '"' {
		if err := json.Unmarshal([]byte(trimmed), &cred); err != nil {
			return nil, fmt.Errorf("parse plaintext credential: %w", err)
		}
		return &cred, nil
	}
	enc, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("base64 decode credential: %w", err)
	}
	plain, err := decryptTCBuffer(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	if err := json.Unmarshal(plain, &cred); err != nil {
		return nil, fmt.Errorf("parse decrypted credential: %w", err)
	}
	return &cred, nil
}

// parseTraeAuth builds the plugin-side auth model from a host auth-file JSON.
// Accepts both shapes seen in the wild:
//
//	nested: {"auth":{"credential":...,"deviceId":...},"account":{"uid":...}}
//	flat:   {"credential":...,"uid":...,"nickname":...}
//
// When a credential blob is present it is decrypted and the runtime fields
// (token/refreshToken/uid/nickname/host) are filled. A usable token is
// required for the auth to be considered valid.
func parseTraeAuth(raw []byte) (*traeAuth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty auth storage")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("storage_parse_error: %w", err)
	}
	var a traeAuth
	if _, nested := probe["auth"]; nested {
		var wrapped struct {
			Auth    traeAuth `json:"auth"`
			Account struct {
				UID      string `json:"uid"`
				Nickname string `json:"nickname"`
				Host     string `json:"host"`
			} `json:"account"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("storage_parse_error: %w", err)
		}
		a = wrapped.Auth
		if a.UserID == "" {
			a.UserID = wrapped.Account.UID
		}
		if a.Nickname == "" {
			a.Nickname = wrapped.Account.Nickname
		}
		if a.Host == "" {
			a.Host = wrapped.Account.Host
		}
	} else {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("storage_parse_error: %w", err)
		}
	}
	// Decrypt the pasted credential blob when present.
	if trimSpace(a.CredentialRaw) != "" {
		cred, err := decryptCredentialString(a.CredentialRaw)
		if err != nil {
			return nil, fmt.Errorf("credential_error: %w", err)
		}
		// Runtime fields written by keepalive (token/refreshToken/expiredAt on
		// the top level) MUST win over the static credential blob: the blob is
		// the client-encrypted snapshot at import time, and a plugin-side
		// token refresh (keepalive.go) updates the top level only. Without
		// this priority the refreshed access token would be silently reverted
		// to the (possibly dead) credential value on every load.
		if a.Token == "" && cred.Token != "" {
			a.Token = cred.Token
		}
		if a.RefreshToken == "" {
			a.RefreshToken = cred.RefreshToken
		}
		if a.ExpiredAt == "" {
			a.ExpiredAt = cred.ExpiredAt
		}
		if a.RefreshExpiredAt == "" {
			a.RefreshExpiredAt = cred.RefreshExpiredAt
		}
		if a.UserID == "" {
			a.UserID = cred.UserID
		}
		if a.Nickname == "" {
			a.Nickname = cred.accountName()
		}
		if a.Host == "" {
			a.Host = cred.Host
		}
	}
	if !a.hasToken() {
		return nil, fmt.Errorf("parse_error: missing access token (paste the credential value from storage.json)")
	}
	return &a, nil
}

// sanitizeHostForCheckin returns a usable check-in host: credential.Host is
// the auth domain (no /trae/api/v2/* routes guaranteed), so the check-in
// module falls back to the CN API host when blank.
func (a *traeAuth) checkinHost() string {
	if trimSpace(a.Host) != "" {
		return trimSpace(a.Host)
	}
	return defaultAPIHost
}

// DetectIDEVersion reads the installed Trae client version from its
// manifest.json (mirrors trae-solo-local-api findManifestPaths/readManifest).
// Returns "" when no manifest is readable.
func DetectIDEVersion() string {
	for _, dir := range manifestCandidates() {
		mf := filepath.Join(dir, "manifest.json")
		raw, err := os.ReadFile(mf)
		if err != nil {
			continue
		}
		var m struct {
			AppVersion string `json:"appVersion"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if m.AppVersion != "" {
			return m.AppVersion
		}
	}
	return ""
}

func manifestCandidates() []string {
	var out []string
	if v := os.Getenv("TRAE_INSTALL_DIR"); v != "" {
		out = append(out, v)
	}
	for _, drive := range []string{"D:", "E:"} {
		out = append(out, filepath.Join(drive, "software", "TRAE SOLO CN"))
		out = append(out, filepath.Join(drive, "software", "Trae CN"))
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		if home, err := os.UserHomeDir(); err == nil {
			local = filepath.Join(home, "AppData", "Local")
		}
	}
	if local != "" {
		for _, name := range []string{"TRAE SOLO CN", "Trae CN", "Trae-CN", "TRAE SOLO", "Trae"} {
			out = append(out, filepath.Join(local, "Programs", name))
		}
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
