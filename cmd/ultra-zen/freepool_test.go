package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/keys"
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
	got := tuiLaunchArgs("zai-org/GLM-5.2", "modelscope", "", modelFlag{
		"modelscope:zai-org/GLM-5.2",
		"opencode-go:deepseek-free",
	}, 0, 20)
	want := "zai-org/GLM-5.2,--provider,modelscope,--free-model,modelscope:zai-org/GLM-5.2,--free-model,opencode-go:deepseek-free"
	if strings.Join(got, ",") != want {
		t.Fatalf("tuiLaunchArgs = %v, want %s", got, want)
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
	list, key, err := loadTUIProvider(client, "openrouter", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "stored-or-key" {
		t.Fatalf("key = %q, want stored key", key)
	}
	if len(list) != 1 || list[0].ID != "vendor/free:free" {
		t.Fatalf("models = %+v, want only free OpenRouter model", list)
	}
}

func TestLoadTUIProviderRejectsUnknown(t *testing.T) {
	if _, _, err := loadTUIProvider(http.DefaultClient, "unknown", "", "", ""); err == nil {
		t.Fatal("unknown TUI provider was accepted")
	}
}
