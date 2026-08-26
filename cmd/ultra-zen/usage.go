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

	"github.com/raketenkater/ultra-zen/internal/proxy"
	"github.com/raketenkater/ultra-zen/internal/usagefmt"
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
//	[OR $0.013 left] [Zen 5h 42% · wk 10% · mo 5%] [Groq 880/1000 · reset 12m]
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
		Providers []proxy.ProviderUsage `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		fmt.Println("no running ultra-zen proxy")
		return
	}
	var parts []string
	for _, p := range payload.Providers {
		parts = append(parts, usagefmt.FormatProviderUsage(p))
	}
	if len(parts) == 0 {
		fmt.Println("no running ultra-zen proxy")
		return
	}
	fmt.Println(strings.Join(parts, " "))
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
