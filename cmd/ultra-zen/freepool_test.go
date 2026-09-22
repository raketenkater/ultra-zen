package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/tui"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSplitFreeModelSpec(t *testing.T) {
	tests := []struct {
		input    string
		provider string
		model    string
	}{
		{"qwen/qwen3-coder:free", "openrouter", "qwen/qwen3-coder:free"},
		{"openrouter:openrouter/free", "openrouter", "openrouter/free"},
		{"opencode:deepseek-v4-flash-free", "opencode-go", "deepseek-v4-flash-free"},
		{"opencode-go:laguna-s-2.1-free", "opencode-go", "laguna-s-2.1-free"},
		{"modelscope:deepseek-ai/DeepSeek-V4-Flash", "modelscope", "deepseek-ai/DeepSeek-V4-Flash"},
		{"groq:llama-3.3-70b-versatile", "groq", "llama-3.3-70b-versatile"},
		{"cerebras:llama-3.3-70b", "cerebras", "llama-3.3-70b"},
		{"huggingface:qwen/qwen3-14b", "huggingface", "qwen/qwen3-14b"},
		{"cohere:command-r", "cohere", "command-r"},
		{"saia:qwen3-coder-next", "saia", "qwen3-coder-next"},
		{"codex:gpt-5", "codex", "gpt-5"},
	}
	for _, test := range tests {
		provider, model, err := splitFreeModelSpec(test.input)
		if err != nil {
			t.Fatalf("splitFreeModelSpec(%q): %v", test.input, err)
		}
		if provider != test.provider || model != test.model {
			t.Fatalf("splitFreeModelSpec(%q) = (%q, %q), want (%q, %q)",
				test.input, provider, model, test.provider, test.model)
		}
	}
}

func TestSplitFreeModelSpecRejectsEmptyModel(t *testing.T) {
	if _, _, err := splitFreeModelSpec("opencode:"); err == nil {
		t.Fatal("expected empty provider-qualified model to fail")
	}
	if _, _, err := splitFreeModelSpec("modelscope:"); err == nil {
		t.Fatal("expected empty modelscope model to fail")
	}
}

func TestFreeRouteStringRoundTrips(t *testing.T) {
	routes := []tui.FreeRoute{
		{Provider: "modelscope", Model: "deepseek-ai/DeepSeek-V4-Flash"},
		{Provider: "openrouter", Model: "openrouter/free"},
		{Provider: "opencode-go", Model: "laguna-s-2.1-free"},
		{Provider: "groq", Model: "llama-3.3-70b-versatile"},
		{Provider: "saia", Model: "qwen3-coder-next"},
	}
	for _, r := range routes {
		provider, model, err := splitFreeModelSpec(r.String())
		if err != nil {
			t.Fatalf("round-trip %q: %v", r.String(), err)
		}
		if provider != r.Provider || model != r.Model {
			t.Fatalf("round-trip %q = (%q, %q), want (%q, %q)", r.String(), provider, model, r.Provider, r.Model)
		}
	}
}

func TestApplySavedFreePoolToDirectLaunch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := []tui.FreeRoute{
		{Provider: "openrouter", Model: "vendor/a:free"},
		{Provider: "opencode-go", Model: "zen-free"},
	}
	if err := tui.SaveFreePool(want); err != nil {
		t.Fatal(err)
	}
	got, requested := applySavedFreePool(nil, "primary-model", false)
	if !requested {
		t.Fatal("saved pool was not enabled")
	}
	if strings.Join(got, ",") != "openrouter:vendor/a:free,opencode-go:zen-free" {
		t.Fatalf("saved pool = %v", got)
	}
}

func TestApplySavedFreePoolHonorsExplicitOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := tui.SaveFreePool([]tui.FreeRoute{{Provider: "openrouter", Model: "saved:free"}}); err != nil {
		t.Fatal(err)
	}

	explicit := modelFlag{"groq:explicit"}
	got, requested := applySavedFreePool(explicit, "primary-model", false)
	if !requested || len(got) != 1 || got[0] != explicit[0] {
		t.Fatalf("explicit pool was replaced: %v, requested=%v", got, requested)
	}

	got, requested = applySavedFreePool(nil, "primary-model", true)
	if requested || len(got) != 0 {
		t.Fatalf("saved pool overrode --worker: %v, requested=%v", got, requested)
	}

	got, requested = applySavedFreePool(nil, "", false)
	if requested || len(got) != 0 {
		t.Fatalf("interactive launch loaded pool before TUI: %v, requested=%v", got, requested)
	}
}

func TestTUILaunchArgsRecordFinalProviderAndPool(t *testing.T) {
	got := tuiLaunchArgs("zai-org/GLM-5.2", "modelscope", "", "", modelFlag{
		"modelscope:zai-org/GLM-5.2",
		"opencode-go:deepseek-free",
	}, 0, 20)
	want := "zai-org/GLM-5.2,--provider,modelscope,--free-model,modelscope:zai-org/GLM-5.2,--free-model,opencode-go:deepseek-free"
	if strings.Join(got, ",") != want {
		t.Fatalf("tuiLaunchArgs = %v, want %s", got, want)
	}

	// "auto" records as nothing (it is the launch default); "none" and explicit
	// ids record verbatim so resume reproduces the original tier split.
	got = tuiLaunchArgs("glm-5.2", "opencode-go", "", "auto", nil, 0, 20)
	want = "glm-5.2,--provider,opencode-go"
	if strings.Join(got, ",") != want {
		t.Fatalf("tuiLaunchArgs auto = %v, want %s", got, want)
	}
	got = tuiLaunchArgs("glm-5.2", "opencode-go", "", "none", nil, 0, 20)
	want = "glm-5.2,--provider,opencode-go,--fast-model,none"
	if strings.Join(got, ",") != want {
		t.Fatalf("tuiLaunchArgs none = %v, want %s", got, want)
	}
	got = tuiLaunchArgs("glm-5.2", "opencode-go", "", "glm-5.3-flash", nil, 0, 20)
	want = "glm-5.2,--provider,opencode-go,--fast-model,glm-5.3-flash"
	if strings.Join(got, ",") != want {
		t.Fatalf("tuiLaunchArgs explicit = %v, want %s", got, want)
	}
}

func TestLoadTUIProviderOpenRouter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "")
	if err := keys.Save("openrouter", "stored-or-key"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer stored-or-key" {
			t.Errorf("Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"data":[{"id":"vendor/free:free"},{"id":"vendor/paid"}]}`)),
			Request: req,
		}, nil
	})}
	list, key, err := loadTUIProvider(client, "openrouter", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if key != "stored-or-key" {
		t.Fatalf("key = %q, want stored key", key)
	}
	// The reload now mirrors the picker (ranked top-100 / full catalog), so it
	// must include paid models too — not just the free-only list. With no ranking
	// data the models sort alphabetically; both are present.
	if len(list) != 2 {
		t.Fatalf("models = %+v, want both free and paid OpenRouter models", list)
	}
	if models.Find(list, "vendor/paid") == nil {
		t.Fatal("paid model missing from TUI reload; picker selection would fail to resolve")
	}
}

func TestLoadTUIProviderRejectsUnknown(t *testing.T) {
	if _, _, err := loadTUIProvider(http.DefaultClient, "unknown", "", "", "", false); err == nil {
		t.Fatal("unknown TUI provider was accepted")
	}
}

// TestResolveAgainstFullCatalogFindsCappedModel is the regression test for
// the launch failure that followed the search screen reading full catalogs:
// selecting xiaomi/mimo-v2.6-pro — real, priced, and shown by the search —
// died with `model "xiaomi/mimo-v2.6-pro" not found`, because the launch
// resolved it against the picker's display list, where OpenRouter's paid
// block is capped to the hundred most-used models.
func TestResolveAgainstFullCatalogFindsCappedModel(t *testing.T) {
	narrow := []models.Model{{ID: "z-ai/glm-5.2", Base: models.OpenRouterBase}}
	full := append(append([]models.Model{}, narrow...),
		models.Model{ID: "xiaomi/mimo-v2.6-pro", Base: models.OpenRouterBase, ContextLength: 262144})

	calls := 0
	got, list, key := resolveAgainstFullCatalog(narrow, "old-key", "xiaomi/mimo-v2.6-pro",
		func() ([]models.Model, string, error) { calls++; return full, "full-key", nil })

	if got == nil {
		t.Fatal("model absent from the narrowed list was not found in the full catalog")
	}
	if got.ID != "xiaomi/mimo-v2.6-pro" {
		t.Errorf("resolved %q, want xiaomi/mimo-v2.6-pro", got.ID)
	}
	if calls != 1 {
		t.Errorf("reload called %d times, want exactly 1", calls)
	}
	// The caller must switch to the full catalog and its key, or everything
	// downstream (advertising, fallbacks) still works off the capped list.
	if len(list) != len(full) {
		t.Errorf("list has %d models, want the full %d", len(list), len(full))
	}
	if key != "full-key" {
		t.Errorf("key = %q, want the reloaded full-key", key)
	}
}

// TestResolveAgainstFullCatalogSkipsReloadOnHit keeps the common path free of
// a second catalog fetch: almost every launch names a model that is already
// in the list.
func TestResolveAgainstFullCatalogSkipsReloadOnHit(t *testing.T) {
	narrow := []models.Model{{ID: "z-ai/glm-5.2", Base: models.OpenRouterBase}}
	got, list, key := resolveAgainstFullCatalog(narrow, "k", "z-ai/glm-5.2",
		func() ([]models.Model, string, error) {
			t.Fatal("reload called for a model already in the list")
			return nil, "", nil
		})
	if got == nil || got.ID != "z-ai/glm-5.2" {
		t.Fatalf("resolved %v, want the model from the narrowed list", got)
	}
	if len(list) != 1 || key != "k" {
		t.Errorf("a hit must not disturb the list or key; got %d models, key %q", len(list), key)
	}
}

// TestResolveAgainstFullCatalogKeepsListWhenReloadFails checks that a
// transient catalog failure does not also destroy the list the caller
// already had — the launch should fall through to its normal handling with
// its original state intact.
func TestResolveAgainstFullCatalogKeepsListWhenReloadFails(t *testing.T) {
	narrow := []models.Model{{ID: "z-ai/glm-5.2", Base: models.OpenRouterBase}}
	got, list, key := resolveAgainstFullCatalog(narrow, "k", "nope/missing",
		func() ([]models.Model, string, error) { return nil, "", errors.New("dial tcp: refused") })
	if got != nil {
		t.Errorf("resolved %v for a model nothing serves, want nil", got)
	}
	if len(list) != 1 || key != "k" {
		t.Errorf("a failed reload disturbed the caller's state: %d models, key %q", len(list), key)
	}
}

// TestResolveAgainstFullCatalogGenuinelyMissing keeps the honest negative:
// a model no provider serves must still resolve to nil after the full
// catalog has been consulted.
func TestResolveAgainstFullCatalogGenuinelyMissing(t *testing.T) {
	narrow := []models.Model{{ID: "z-ai/glm-5.2"}}
	full := []models.Model{{ID: "z-ai/glm-5.2"}, {ID: "xiaomi/mimo-v2.6-pro"}}
	got, _, _ := resolveAgainstFullCatalog(narrow, "k", "acme/not-a-model",
		func() ([]models.Model, string, error) { return full, "k2", nil })
	if got != nil {
		t.Errorf("resolved %v for a model that does not exist, want nil", got)
	}
}
