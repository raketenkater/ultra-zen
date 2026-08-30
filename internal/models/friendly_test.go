package models

import "testing"

func TestFriendlyName(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		// opencode Zen ids.
		{"deepseek-v4-flash", "DeepSeek V4 Flash"},
		{"deepseek-v4-flash-free", "DeepSeek V4 Flash"},
		{"glm-5.1", "GLM 5.1"},
		{"kimi-k2.6", "Kimi K2.6"},
		{"mimo-v2.5", "Mimo V2.5"},
		{"gpt-5.6-luna", "GPT 5.6 Luna"},
		{"grok-4.5", "Grok 4.5"},
		{"hy3", "Hy3"},
		{"hy3-preview", "Hy3 Preview"},
		// OpenRouter :free slugs (vendor dropped, size kept).
		{"poolside/laguna-s-2.1:free", "Laguna S 2.1"},
		{"cohere/north-mini-code:free", "North Mini Code"},
		{"google/gemma-4-26b-a4b-it:free", "Gemma 4 26B A4B IT"},
		{"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free", "Nemotron 3 Nano Omni 30B A3B Reasoning"},
		{"openai/gpt-oss-20b:free", "GPT OSS 20B"},
		{"openrouter/free", "OpenRouter Free"},
		// ModelScope / HF owner/name (owner dropped).
		{"zai-org/GLM-5.2", "GLM 5.2"},
		{"Qwen/Qwen3-235B-A22B", "Qwen3 235B A22B"},
		{"MiniMax/MiniMax-M3", "MiniMax M3"},
		{"deepseek-ai/DeepSeek-V4-Pro", "DeepSeek V4 Pro"},
	}
	for _, tc := range cases {
		if got := FriendlyName(tc.id); got != tc.want {
			t.Errorf("FriendlyName(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
