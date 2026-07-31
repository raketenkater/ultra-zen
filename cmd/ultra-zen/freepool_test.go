package main

import (
	"testing"

	"github.com/raketenkater/ultra-zen/internal/tui"
)

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
