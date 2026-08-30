// Package codex reads the credential and model configuration the installed
// codex CLI (https://github.com/openai/codex) keeps on this machine, so
// ultra-zen can use a ChatGPT Plus/Pro subscription without any extra setup:
// no URL, no API key, no ChatMock process — the subscription itself is the
// backend.
//
// The codex CLI stores its login under CODEX_HOME (default ~/.codex):
//
//   - auth.json     — {"auth_mode":"chatgpt","tokens":{"access_token",
//     "refresh_token","account_id",...}}. The access_token is a JWT that the
//     ChatGPT backend accepts as "Authorization: Bearer <token>", paired with
//     a ChatGPT-Account-ID header.
//   - config.toml   — the user's chosen model (model = "gpt-5.6-sol"), read as
//     the TUI's primary-codex-model hint.
//   - models_cache.json — the codex CLI's own cached model list (same shape as
//     the live GET /models response). Used as a local fallback for the model
//     catalog.
//
// The token is used as a Bearer credential and is NEVER logged, printed, or
// written to ultra-zen's key store.
package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultHome is the codex CLI's config directory when CODEX_HOME is unset.
const DefaultHome = ".codex"

// chatgptClientID is the public OAuth client the codex CLI uses to refresh its
// ChatGPT tokens. It is the same value baked into the codex binary and into
// the third-party integrations that talk to the same backend (e.g. Simon
// Willison's llm-openai-via-codex).
const chatgptClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// oauthTokenURL issues fresh access tokens from a refresh_token.
const oauthTokenURL = "https://auth.openai.com/oauth/token"

// authFile is the subset of ~/.codex/auth.json ultra-zen reads. Only the
// ChatGPT mode is supported; an OpenAI API key (auth_mode "legacy_key" or a
// token-less OPENAI_API_KEY) is out of scope here because it already works via
// the free-tier / BYO-key providers.
type authFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh"`
}

// Auth is a usable ChatGPT-subscription credential.
type Auth struct {
	AccessToken  string // Bearer credential for the ChatGPT backend
	RefreshToken string // exchanged for a new access token on expiry
	AccountID    string // sent as ChatGPT-Account-ID
	Path         string // auth.json this was read from
}

// Home returns the codex config directory (CODEX_HOME override, else ~/.codex).
func Home() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return DefaultHome
	}
	return filepath.Join(home, DefaultHome)
}

// AuthPath returns the path to the codex auth.json under CODEX_HOME.
func AuthPath() string { return filepath.Join(Home(), "auth.json") }

// Detect reads and validates ~/.codex/auth.json. It returns ok=false when the
// codex CLI is not installed/logged in or the login is not a ChatGPT
// subscription (e.g. an API-key login), so the caller can fall back to the
// existing local-endpoint codex flow. No error is returned for a missing file —
// that is the "not installed" signal, not a failure.
func Detect() (Auth, bool) {
	data, err := os.ReadFile(AuthPath())
	if err != nil {
		return Auth{}, false
	}
	var f authFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Auth{}, false
	}
	// Only the ChatGPT subscription mode gives us a bearer token + account id.
	// auth_mode can be "chatgpt" (Plus/Pro login) — anything else (e.g. a raw
	// API key) does not use the ChatGPT backend.
	if f.AuthMode != "chatgpt" || f.Tokens.AccessToken == "" || f.Tokens.AccountID == "" {
		return Auth{}, false
	}
	return Auth{
		AccessToken:  f.Tokens.AccessToken,
		RefreshToken: f.Tokens.RefreshToken,
		AccountID:    f.Tokens.AccountID,
		Path:         AuthPath(),
	}, true
}

// NeedsRefresh reports whether the access token is close to expiring. The
// ChatGPT backend rejects expired/near-expired tokens, so ultra-zen refreshes
// proactively rather than letting a launch die mid-session. A JWT carries exp
// in its payload; if it cannot be decoded (e.g. an opaque token), refresh is
// only attempted when the backend itself returns 401.
func (a Auth) NeedsRefresh() bool {
	exp, ok := jwtExpiry(a.AccessToken)
	if !ok {
		return false
	}
	// Refresh when under 10 minutes remain (a session can run long).
	return time.Until(exp) < 10*time.Minute
}

// jwtExpiry decodes the exp claim from an unverified JWT payload (the middle
// dot-separated, base64url segment). The token is used as a credential, not a
// trust anchor, so we only read the expiry and never validate the signature.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload := parts[1]
	if rem := len(payload) % 4; rem != 0 {
		payload += strings.Repeat("=", 4-rem)
	}
	decoded, err := decodeBase64URL(payload)
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

func decodeBase64URL(s string) ([]byte, error) {
	// base64.RawURLEncoding tolerates unpadded base64url, which is what a JWT
	// payload uses (no trailing '='). Adding back any missing padding first
	// keeps the decoder unambiguous.
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

// Version returns the installed codex CLI version string, or "" when the CLI
// cannot be located. The ChatGPT backend expects a client_version query on GET
// /models; matching the installed CLI keeps the catalog accurate. The codex
// binary prints "codex-cli 0.147.0"; we parse the second field and fall back to
// the whole output for older one-field formats.
func Version() string {
	out, err := exec.Command("codex", "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "codex-cli" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	if len(fields) == 1 {
		return fields[0]
	}
	return ""
}

// PrimaryModel returns the model id the codex CLI is configured to use
// (config.toml's `model = "..."`), or "" when unset. It is a display hint for
// the TUI so a single keypress can launch the user's already-preferred model;
// it is not an authoritative catalog (that comes from /models).
func PrimaryModel() string {
	data, err := os.ReadFile(filepath.Join(Home(), "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "model") || !strings.Contains(line, "=") {
			continue
		}
		value := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
		value = strings.Trim(value, `"'`)
		if value != "" && !strings.HasPrefix(value, "#") {
			return value
		}
	}
	return ""
}

// Refresh exchanges the stored refresh_token for a new access_token (and a
// rotating refresh_token), then atomically rewrites auth.json with mode 0600 so
// the codex CLI keeps working. This is the same OAuth flow the codex CLI itself
// uses. httpClient is used for the token request; a nil client uses a default.
//
// On success the caller should re-run Detect to pick up the new credential.
// On failure the existing token is left untouched and an error is returned so
// the caller can surface "codex login expired, run `codex login`".
func Refresh(httpClient *http.Client, a Auth) error {
	if a.RefreshToken == "" {
		return fmt.Errorf("no refresh token in %s; re-run `codex login`", a.Path)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	form := "grant_type=refresh_token&client_id=" + chatgptClientID + "&refresh_token=" + a.RefreshToken
	req, err := http.NewRequest(http.MethodPost, oauthTokenURL, strings.NewReader(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh codex token: %w", err)
	}
	defer resp.Body.Close()
	// Cap the body read; the response is a small JSON object.
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(http.MaxBytesReader(nil, resp.Body, 64<<10)); err != nil {
		return fmt.Errorf("read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh codex token: %s: %s", resp.Status, strings.TrimSpace(buf.String()))
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(buf.Bytes(), &t); err != nil {
		return fmt.Errorf("parse refresh response: %w", err)
	}
	if t.AccessToken == "" {
		return fmt.Errorf("refresh response contained no access_token")
	}
	if t.RefreshToken != "" {
		a.RefreshToken = t.RefreshToken
	}
	a.AccessToken = t.AccessToken
	return writeAuth(a.Path, a)
}

// writeAuth atomically rewrites auth.json with the refreshed tokens, preserving
// mode 0600. The file is owned by the codex CLI; rewriting it in place keeps a
// single source of truth so the CLI and ultra-zen stay in sync.
func writeAuth(path string, a Auth) error {
	// Re-read the current file so unknown fields are preserved across the
	// rewrite (the codex CLI may add keys ultra-zen does not model).
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s for refresh rewrite: %w", path, err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s for refresh rewrite: %w", path, err)
	}
	tokens, _ := f["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = a.AccessToken
	if a.RefreshToken != "" {
		tokens["refresh_token"] = a.RefreshToken
	}
	f["tokens"] = tokens
	f["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	// Write the temp file next to the target, then rename over it, so a crash
	// mid-write never leaves a truncated auth.json.
	tmp := path + ".ultra-zen-tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
