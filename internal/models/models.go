// Package models discovers the models available on the opencode Zen gateway.
// The gateway exposes two tiers: the "go" tier (https://opencode.ai/zen/go/v1,
// the opencode-go provider, credits required) and the main tier
// (https://opencode.ai/zen/v1, which also hosts the *-free models).
package models

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Base URLs for the two Zen tiers.
const (
	GoBase   = "https://opencode.ai/zen/go/v1"
	MainBase = "https://opencode.ai/zen/v1"
)

// Model is one selectable model.
type Model struct {
	ID   string // gateway model id, e.g. "glm-5.1"
	Name string // human-friendly name
	Base string // gateway base URL this model lives on
	Free bool   // whether the model is a *-free variant
}

// List fetches all usable models for the given API key: every model on the
// opencode-go tier, plus the free models on the main tier. Non-free main-tier
// models are excluded because they require a separate balance the opencode-go
// key does not cover.
func List(httpClient *http.Client, apiKey string) ([]Model, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	goModels, err := fetchIDs(httpClient, GoBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("go tier: %w", err)
	}
	mainModels, err := fetchIDs(httpClient, MainBase, apiKey)
	if err != nil {
		return nil, fmt.Errorf("main tier: %w", err)
	}

	var out []Model
	for _, id := range goModels {
		out = append(out, Model{ID: id, Name: pretty(id), Base: GoBase, Free: false})
	}
	for _, id := range mainModels {
		if !strings.HasSuffix(id, "-free") {
			continue
		}
		out = append(out, Model{ID: id, Name: pretty(id), Base: MainBase, Free: true})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return !out[i].Free // paid go-tier first, free second
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func fetchIDs(c *http.Client, base, key string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s/models: %s: %s", base, resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// pretty turns a model id into a display name. We keep it close to the id but
// tidy a couple of common suffixes.
func pretty(id string) string {
	name := id
	name = strings.TrimSuffix(name, "-free")
	return name
}

// Find returns the model with the given id, or nil.
func Find(list []Model, id string) *Model {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}