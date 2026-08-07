// Package models: recheck.go re-polls denied models so a daily-limit or
// transient access denial does not keep a model hidden for the full TTL when
// the provider starts serving it again within the same session.
//
// The TTL in unavailable.go is a safety net for stores that are only read on
// launch. This poller is the live-side complement: while an ultra-zen session
// runs it periodically re-fetches the provider catalog and clears the denial
// the moment the model reappears — no 24h wait to see it again.
package models

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// RecheckInterval is how often the poller re-checks denied models.
const RecheckInterval = 30 * time.Second

// recheckMu serializes access to the denied snapshot and next-clear map.
var recheckMu sync.Mutex

// StartRecheckPoller launches a background goroutine that every
// RecheckInterval re-fetches the provider catalog for each currently-denied
// model and clears the denial when the model is listed again. It stops when
// ctx is cancelled (session teardown). apiKey must be the key for the denied
// providers' gateway; httpClient should be the session's shared client.
//
// Only catalog re-listing is checked, not account-level access: a denial is
// cleared when the provider serves the model id again, even if the account is
// still not permitted. That matches the "is it back on the catalog" model the
// TUI uses to decide whether to show the row at all.
func StartRecheckPoller(ctx context.Context, httpClient *http.Client, apiKey string) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	go func() {
		ticker := time.NewTicker(RecheckInterval)
		defer ticker.Stop()
		// First pass immediately so a denial recorded just before launch is
		// re-checked without waiting a full interval.
		recheckOnce(httpClient, apiKey)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recheckOnce(httpClient, apiKey)
			}
		}
	}()
}

// recheckOnce re-fetches the catalog for every provider that currently has a
// denied model and clears those that reappeared. A provider whose catalog fetch
// fails is left alone; the next tick retries it.
func recheckOnce(httpClient *http.Client, apiKey string) {
	recheckMu.Lock()
	denied := deniedSnapshot()
	recheckMu.Unlock()
	if len(denied) == 0 {
		return
	}

	// Group denied models by provider so each provider's catalog is fetched at
	// most once per tick.
	byProvider := map[string][]string{}
	for provider, models := range denied {
		byProvider[provider] = append(byProvider[provider], models...)
	}
	for provider, deniedIDs := range byProvider {
		base := BaseForProvider(provider)
		if base == "" {
			continue
		}
		recheckProvider(httpClient, base, apiKey, provider, deniedIDs)
	}
}

// recheckProvider clears the given provider's denied models that are listed in
// the catalog served at base. Splitting this out lets tests drive a provider
// against a fake base URL without touching the const GoBase.
func recheckProvider(httpClient *http.Client, base, apiKey, provider string, deniedIDs []string) {
	entries, err := fetchEntries(httpClient, base, apiKey)
	if err != nil {
		// Transient catalog failure; keep the denials and retry next tick.
		return
	}
	served := make(map[string]bool, len(entries))
	for _, e := range entries {
		served[e.ID] = true
	}
	for _, id := range deniedIDs {
		if served[id] {
			_ = ClearUnavailableModel(provider, id)
		}
	}
}

// deniedSnapshot returns the currently-denied provider→model-ids map under the
// store lock, applying the TTL expiry so a denial that already lapsed on its
// own is not re-checked (it is already gone from FilterUnavailable).
func deniedSnapshot() map[string][]string {
	unavailableMu.Lock()
	entries := loadUnavailableLocked()
	unavailableMu.Unlock()
	out := map[string][]string{}
	for _, e := range entries {
		out[e.Provider] = append(out[e.Provider], e.Model)
	}
	return out
}
