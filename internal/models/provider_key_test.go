package models

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/keys"
)

type modelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderKeyUsesSavedOpenCodeKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := keys.Save("opencode-go", "saved-zen-key"); err != nil {
		t.Fatal(err)
	}
	if got := ProviderKey("opencode-go", "", ""); got != "saved-zen-key" {
		t.Fatalf("ProviderKey = %q, want saved opencode key", got)
	}
	if got := ProviderKey("opencode-go", "", "auth-key"); got != "auth-key" {
		t.Fatalf("explicit Zen key = %q, want auth-key", got)
	}
}

func TestSAIAProviderUsesEnvironmentThenStoredKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SAIA_API_KEY", "saia-env-key")
	if got := ProviderKey("saia", "", ""); got != "saia-env-key" {
		t.Fatalf("SAIA env key = %q, want saia-env-key", got)
	}
	t.Setenv("SAIA_API_KEY", "")
	if err := keys.Save("saia", "saia-stored-key"); err != nil {
		t.Fatal(err)
	}
	if got := ProviderKey("saia", "", ""); got != "saia-stored-key" {
		t.Fatalf("SAIA stored key = %q, want saia-stored-key", got)
	}
}

func TestListKeepsZenFreeModelsWhenGoTierUnavailable(t *testing.T) {
	client := &http.Client{Transport: modelRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := `{"data":[{"id":"deepseek-free"}]}`
		if strings.Contains(req.URL.Path, "/zen/go/") {
			status = http.StatusPaymentRequired
			body = `{"error":"no Go credits"}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	list, err := List(client, "zen-key")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "deepseek-free" || !list[0].Free {
		t.Fatalf("List = %+v, want surviving Zen free model", list)
	}
}
