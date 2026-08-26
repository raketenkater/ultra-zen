package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// cmdUsage prints a single compact statusline summarizing per-provider usage,
// for use as a Claude Code statusline hook. It reads the running proxy's address
// from ~/.cache/ultra-zen/proxy.json (written by the proxy on start) and GETs
// {url}/v1/usage. When no proxy file exists it prints "no running ultra-zen
// proxy" to stdout and exits 0 so the statusline never error-spams.
//
// Usage note: wiring this into Claude Code's statusline is done via a settings
// hook that runs `ultra-zen usage` (or `ultra-zen statusline`); the output is a
// single line suitable for the statusline:
//
//	[OpenRouter $0.013 left] [Zen 5h 42%] [Groq 912 req] [SAIA 880/d]
//	[Cohere 642/1000] [Cerebras hit] [ModelScope —]
func cmdUsage() {
	path := proxyInfoPath()
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("no running ultra-zen proxy")
		return
	}
	var info struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &info); err != nil || info.URL == "" {
		fmt.Println("no running ultra-zen proxy")
		return
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(info.URL + "/v1/usage")
	if err != nil {
		fmt.Println("no running ultra-zen proxy")
		return
	}
	defer resp.Body.Close()
	var payload struct {
		Providers []proxyUsageView `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		fmt.Println("no running ultra-zen proxy")
		return
	}
	var parts []string
	for _, p := range payload.Providers {
		parts = append(parts, renderUsagePart(p))
	}
	if len(parts) == 0 {
		fmt.Println("no running ultra-zen proxy")
		return
	}
	fmt.Println(strings.Join(parts, " "))
}

// proxyUsageView is a local mirror of proxy.ProviderUsage for decoding; reusing
// the proxy type directly would force an import cycle-free copy, so we decode
// the fields we render.
type proxyUsageView struct {
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	Remaining    *float64 `json:"remaining,omitempty"`
	Percent      *int    `json:"percent,omitempty"`
	RequestsUsed *int64  `json:"requestsUsed,omitempty"`
	RequestsLimit *int64 `json:"requestsLimit,omitempty"`
	Rolling      *windowView `json:"rolling,omitempty"`
	Weekly       *windowView `json:"weekly,omitempty"`
	Monthly      *windowView `json:"monthly,omitempty"`
	Exhausted    bool    `json:"exhausted"`
	Detail       string  `json:"detail,omitempty"`
}

type windowView struct {
	Percent  int    `json:"percent"`
	ResetsAt string `json:"resetsAt"`
}

// renderUsagePart formats one provider into a bracketed statusline token.
func renderUsagePart(p proxyUsageView) string {
	title := p.Name
	if p.Exhausted {
		return fmt.Sprintf("[%s hit]", title)
	}
	switch p.Kind {
	case "credits":
		// Prefer Zen's rolling 5h window percent; otherwise OpenRouter remaining.
		if p.Rolling != nil {
			return fmt.Sprintf("[%s 5h %d%%]", title, p.Rolling.Percent)
		}
		if p.Remaining != nil {
			return fmt.Sprintf("[%s $%.3f left]", title, *p.Remaining)
		}
	case "requests":
		if p.RequestsLimit != nil && p.RequestsUsed != nil {
			return fmt.Sprintf("[%s %d/%d]", title, *p.RequestsUsed, *p.RequestsLimit)
		}
		if p.RequestsUsed != nil {
			return fmt.Sprintf("[%s %d req]", title, *p.RequestsUsed)
		}
		if p.Percent != nil {
			return fmt.Sprintf("[%s %d%%]", title, *p.Percent)
		}
	}
	if p.Detail == "" {
		return fmt.Sprintf("[%s —]", title)
	}
	return fmt.Sprintf("[%s —]", title)
}

// proxyInfoPath resolves ~/.cache/ultra-zen/proxy.json.
func proxyInfoPath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		if u, err := user.Current(); err == nil {
			dir = filepath.Join(u.HomeDir, ".cache")
		} else {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".cache")
		}
	}
	return filepath.Join(dir, "ultra-zen", "proxy.json")
}
