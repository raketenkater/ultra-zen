// Provider-key setup: the `setup providers` subcommand. Prints a per-provider
// key status table (which source supplies each key — env var, per-user store,
// system store, opencode's auth.json, codex login — plus the tier and the live
// usage row when a proxy is running), then, on a real terminal, offers to
// configure providers whose key is missing.
//
// Key entry is masked (the TUI's own bubbletea prompt — no new dependency,
// no stty subprocess). Every new key is validated against the provider's
// cheapest authenticated endpoint before it is stored via internal/keys, and
// is never printed back. Interactive prompts run only when stdin is a real
// terminal: a piped run (curl | sh, scripts, tests) prints the table plus the
// equivalent non-interactive recipes and never reads stdin that does not
// belong to it. Re-running is always safe — configured providers are skipped.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/raketenkater/ultra-zen/internal/auth"
	"github.com/raketenkater/ultra-zen/internal/codex"
	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/proxy"
	"github.com/raketenkater/ultra-zen/internal/tui"
	"github.com/raketenkater/ultra-zen/internal/usagefmt"
)

// setupProviderOrder drives both the status table and the configure loop: the
// default provider first, then OpenRouter, then the remaining BYO free tiers.
// codex is not in the list — it uses a codex login or a URL, never a stored
// key, so there is nothing to configure here; it is shown in the table when
// its login is detected.
var setupProviderOrder = []string{
	"opencode-go", "openrouter", "modelscope", "groq",
	"cerebras", "huggingface", "cohere", "saia",
}

// setupProbeOverrides retargets the two validation endpoints that have no
// models.FreeTierProviders entry. Tests point them at a local fake server;
// production leaves them empty and probeURL falls back to the real bases.
var (
	setupProbeOpenRouter string // empty = models.OpenRouterBase + "/key"
	setupProbeZen        string // empty = models.GoBase + "/models"
)

// stdinIsTerminal reports whether setup may prompt: only a real terminal
// gets interactive questions. A pipe (curl | sh, scripts, tests) never does.
// /dev/null is a character device too, so the ModeCharDevice check alone
// would treat `uz setup providers </dev/null` as interactive — exclude it by
// comparing the device identity.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Open(os.DevNull); err == nil {
		nfi, err := null.Stat()
		null.Close()
		if err == nil && os.SameFile(fi, nfi) {
			return false
		}
	}
	return true
}

// opencodeAuthKey returns the opencode-go key from opencode's auth.json, or
// "". Load failures just mean "no key here" for the status table.
func opencodeAuthKey() string {
	store, err := auth.Load("")
	if err != nil {
		return ""
	}
	k, err := auth.KeyFor(store, "opencode-go")
	if err != nil {
		return ""
	}
	return k
}

// keySourceLabel names where a provider's key currently comes from, in the
// launcher's own precedence: env var → per-user store → system store, with
// opencode's auth.json as the opencode-go fallback. Never returns the key.
func keySourceLabel(provider string) string {
	if def, ok := models.FreeTierProviders[provider]; ok && os.Getenv(def.EnvKey) != "" {
		return "set (env)"
	}
	if provider == "openrouter" && os.Getenv("OPENROUTER_API_KEY") != "" {
		return "set (env)"
	}
	switch {
	case keys.HasIn(provider, keys.StoreUser):
		return "set (user store)"
	case keys.HasIn(provider, keys.StoreSystem):
		return "set (system store)"
	case provider == "opencode-go" && opencodeAuthKey() != "":
		return "set (opencode auth)"
	}
	return "not set"
}

// setupTier is the static tier label for the table's TIER column.
func setupTier(provider string) string {
	if provider == "opencode-go" {
		return "credits (go tier)"
	}
	return "free tier"
}

// setupKeyHint returns where to get a key for provider.
func setupKeyHint(provider string) string {
	if def, ok := models.FreeTierProviders[provider]; ok {
		return def.KeyHint
	}
	switch provider {
	case "openrouter":
		return "https://openrouter.ai/keys"
	case "opencode-go":
		return "https://opencode.ai/auth"
	}
	return ""
}

// probeURL returns the validation endpoint for provider ("" = none).
func probeURL(provider string) string {
	if def, ok := models.FreeTierProviders[provider]; ok {
		return def.Base + "/models"
	}
	switch provider {
	case "openrouter":
		if setupProbeOpenRouter != "" {
			return setupProbeOpenRouter
		}
		return models.OpenRouterBase + "/key"
	case "opencode-go":
		if setupProbeZen != "" {
			return setupProbeZen
		}
		return models.GoBase + "/models"
	}
	return ""
}

// setupValidateKey checks a candidate key against the provider's cheapest
// authenticated endpoint (GET /models, or OpenRouter's GET /key) before it is
// stored, so a typo never silently poisons the key store.
func setupValidateKey(httpClient *http.Client, provider, key string) error {
	url := probeURL(provider)
	if url == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s (key rejected)", url, resp.Status)
	}
	return nil
}

// setupStoreKey validates then persists one key, clearing any cached model
// denials for the provider (a new key may have access the old one lacked).
// The key is never printed back.
func setupStoreKey(httpClient *http.Client, provider, key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("empty key")
	}
	if err := setupValidateKey(httpClient, provider, key); err != nil {
		return err
	}
	if err := keys.Save(provider, key); err != nil {
		return err
	}
	_ = models.ClearUnavailable(provider)
	return nil
}

// readUsageRows returns one usagefmt-formatted token per provider from the
// running proxy (if any), keyed by provider name. No proxy, or any failure,
// yields an empty map — the table simply shows no usage column values.
func readUsageRows() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(proxyInfoPath())
	if err != nil {
		return out
	}
	var info struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &info); err != nil || info.URL == "" {
		return out
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(info.URL + "/v1/usage")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var payload struct {
		Providers []proxy.ProviderUsage `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return out
	}
	for _, p := range payload.Providers {
		if tok := usagefmt.FormatProviderUsage(p); tok != "" {
			out[p.Name] = tok
		}
	}
	return out
}

// cmdSetupProviders implements `setup providers`: print the status table,
// then — only on a real terminal — offer to configure providers whose key is
// missing. Non-interactive runs print the equivalent `uz keys set` recipes
// instead of ever reading stdin.
func cmdSetupProviders(httpClient *http.Client) {
	setupProvidersRun(httpClient, stdinIsTerminal())
}

// setupProvidersRun is the testable body of cmdSetupProviders; interactive
// says whether prompts may happen (tests pass false explicitly so a `go test`
// run on an interactive terminal stays hermetic).
func setupProvidersRun(httpClient *http.Client, interactive bool) {
	usage := readUsageRows()

	fmt.Fprintln(stdout, "Provider keys")
	fmt.Fprintln(stdout, strings.Repeat("-", 76))
	fmt.Fprintf(stdout, "%-13s %-19s %-18s %s\n", "PROVIDER", "KEY", "TIER", "USAGE")
	fmt.Fprintln(stdout, strings.Repeat("-", 76))
	for _, p := range setupProviderOrder {
		fmt.Fprintf(stdout, "%-13s %-19s %-18s %s\n", p, keySourceLabel(p), setupTier(p), usage[p])
	}
	if _, ok := codex.Detect(); ok {
		fmt.Fprintf(stdout, "%-13s %-19s %-18s %s\n", "codex", "set (codex login)", "ChatGPT sub", usage["codex"])
	}
	fmt.Fprintln(stdout, strings.Repeat("-", 76))
	fmt.Fprintf(stdout, "keys live in %s (mode 0600)\n", keys.Path())

	var missing []string
	for _, p := range setupProviderOrder {
		if keySourceLabel(p) == "not set" {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "All provider keys configured. Run: uz")
		return
	}
	if !interactive {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Add a key non-interactively:")
		fmt.Fprintln(stdout, "  uz keys set <provider> -      # paste key, Ctrl-D to finish")
		fmt.Fprintln(stdout, "  echo <key> | uz keys set <provider> -")
		return
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintf(stdout, "No key yet: %s\n", strings.Join(missing, ", "))
	fmt.Fprint(stdout, "Configure one now? [y/N] ")
	if !setupConfirm() {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Run `uz setup providers` any time, or: uz")
		return
	}
	setupConfigureLoop(httpClient, missing)
}

// setupConfigureProvider validates and stores one provider key. Factored out
// of the interactive loop so tests can exercise the validate-then-store path
// without a terminal.
func setupConfigureProvider(httpClient *http.Client, provider, key string) error {
	fmt.Fprintf(stdout, "checking %s ... ", probeURL(provider))
	if err := setupStoreKey(httpClient, provider, key); err != nil {
		fmt.Fprintf(stdout, "FAILED\n  %v\n", err)
		fmt.Fprintln(stdout, "  not stored. Check the key and re-run `uz setup providers`.")
		return err
	}
	fmt.Fprintln(stdout, "ok")
	fmt.Fprintf(stdout, "stored %s key in %s\n", provider, keys.Path())
	return nil
}

// setupConfirm reads a y/N answer from the terminal. EOF/cancel is "no".
func setupConfirm() bool {
	var ans string
	if _, err := fmt.Fscanln(stdin, &ans); err != nil {
		fmt.Fprintln(stdout, "")
		return false
	}
	return strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes")
}

// setupConfigureLoop walks the missing providers. Each key is typed into a
// masked bubbletea prompt (Enter skips a provider, Esc/q aborts the loop),
// validated against the provider's cheapest endpoint, then stored via
// internal/keys. The key is never echoed or printed back.
func setupConfigureLoop(httpClient *http.Client, missing []string) {
	stored := 0
	for _, p := range missing {
		fmt.Fprintln(stdout, "")
		if hint := setupKeyHint(p); hint != "" {
			fmt.Fprintf(stdout, "%s — get a key at %s\n", p, hint)
		} else {
			fmt.Fprintf(stdout, "%s\n", p)
		}
		key := setupPromptKey(p)
		if key == "" {
			fmt.Fprintln(stdout, "skipped")
			continue
		}
		if setupConfigureProvider(httpClient, p, key) == nil {
			stored++
		}
	}
	fmt.Fprintln(stdout, "")
	if stored > 0 {
		fmt.Fprintf(stdout, "stored %d key(s). Run: uz\n", stored)
	} else {
		fmt.Fprintln(stdout, "No keys stored. Run `uz setup providers` any time, or: uz")
	}
}

// setupPromptKey reads one key with masked echo via the TUI's single-line
// bubbletea prompt (the raw-mode handling already ships in the binary; no
// stty subprocess, no new dependency). "" = skipped or no terminal.
func setupPromptKey(provider string) string {
	name := provider
	if provider == "opencode-go" {
		name = "opencode Zen"
	}
	hint := setupKeyHint(provider)
	if hint != "" {
		hint = "get one at " + hint
	}
	return tui.PromptKey("Set "+name+" API key", hint, true)
}
