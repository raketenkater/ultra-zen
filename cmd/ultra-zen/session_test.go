package main

import (
	"os"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/session"
)

func TestSessionClaudeArgsPinsAMintedSessionID(t *testing.T) {
	spec := &sessionSpec{ID: "072e63a1-819a-4682-a742-559695c3cd76"}
	got, err := sessionClaudeArgs(spec, nil, []string{"--append-system-prompt", "x"})
	if err != nil {
		t.Fatalf("sessionClaudeArgs: %v", err)
	}
	if len(got) < 2 || got[0] != "--session-id" || got[1] != spec.ID {
		t.Fatalf("want --session-id %s first, got %v", spec.ID, got)
	}
	// The rest of the invocation must survive.
	if got[2] != "--append-system-prompt" {
		t.Errorf("existing args lost: %v", got)
	}
}

func TestSessionClaudeArgsResumesWithTheOriginalID(t *testing.T) {
	spec := &sessionSpec{ID: "072e63a1-819a-4682-a742-559695c3cd76", Resume: true}
	got, err := sessionClaudeArgs(spec, nil, nil)
	if err != nil {
		t.Fatalf("sessionClaudeArgs: %v", err)
	}
	if len(got) != 2 || got[0] != "--resume" || got[1] != spec.ID {
		t.Fatalf("want --resume %s, got %v", spec.ID, got)
	}
}

// Forking mints a new session ID, which moves the journal path. Every cached
// agent would be silently discarded, so this must fail loudly.
func TestSessionClaudeArgsRefusesForkSessionOnResume(t *testing.T) {
	spec := &sessionSpec{ID: "072e63a1-819a-4682-a742-559695c3cd76", Resume: true}
	_, err := sessionClaudeArgs(spec, []string{"--fork-session"}, nil)
	if err == nil {
		t.Fatal("--fork-session was accepted alongside a resume")
	}
	if !strings.Contains(err.Error(), "fork-session") {
		t.Errorf("error does not name the offending flag: %v", err)
	}
}

func TestSessionClaudeArgsYieldsToAUserSuppliedSession(t *testing.T) {
	spec := &sessionSpec{ID: "072e63a1-819a-4682-a742-559695c3cd76"}
	for _, user := range [][]string{{"--session-id", "x"}, {"--resume", "y"}, {"-r"}} {
		got, err := sessionClaudeArgs(spec, user, []string{"keep"})
		if err != nil {
			t.Fatalf("sessionClaudeArgs(%v): %v", user, err)
		}
		if len(got) != 1 || got[0] != "keep" {
			t.Errorf("ultra-zen overrode the user's own session flag %v: %v", user, got)
		}
	}
}

func TestSessionResumePromptNamesTheRunAndScript(t *testing.T) {
	spec := &sessionSpec{
		ID:       "072e63a1-819a-4682-a742-559695c3cd76",
		Resume:   true,
		Workflow: &session.Workflow{RunID: "wf_894b5285-5d3", ScriptPath: "/tmp/deep-research.js"},
	}
	prompt := sessionResumePrompt(spec)
	if !strings.Contains(prompt, "wf_894b5285-5d3") || !strings.Contains(prompt, "/tmp/deep-research.js") {
		t.Fatalf("prompt missing run id or script path: %s", prompt)
	}
}

func TestSessionResumePromptEmptyWithoutARecordedRun(t *testing.T) {
	spec := &sessionSpec{ID: "072e63a1-819a-4682-a742-559695c3cd76", Resume: true}
	if got := sessionResumePrompt(spec); got != "" {
		t.Errorf("expected no prompt without a workflow run, got: %s", got)
	}
}

func TestStripResumeArgsDropsFlagAndValue(t *testing.T) {
	got := stripResumeArgs([]string{"glm-5.1", "--resume-session", "latest", "--worker", "kimi"})
	want := []string{"glm-5.1", "--worker", "kimi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stripResumeArgs = %v, want %v", got, want)
	}
	got = stripResumeArgs([]string{"glm-5.1", "--resume-session=latest"})
	if len(got) != 1 || got[0] != "glm-5.1" {
		t.Errorf("stripResumeArgs did not drop --resume-session=value form: %v", got)
	}
}

func TestParseSessionResumeArgsSplitsTargetFromOverrides(t *testing.T) {
	target, overrides := parseSessionResumeArgs([]string{"latest", "--provider", "openrouter", "--worker", "kimi"})
	if target != "latest" {
		t.Errorf("target = %q, want latest", target)
	}
	want := "--provider,openrouter,--worker,kimi"
	if strings.Join(overrides, ",") != want {
		t.Errorf("overrides = %v, want %s", overrides, want)
	}
}

func TestApplyResumeOverridesReplacesExistingValue(t *testing.T) {
	recorded := []string{"glm-5.1", "--provider", "opencode-go"}
	got := applyResumeOverrides(recorded, []string{"--provider", "openrouter"})
	want := "glm-5.1,--provider,openrouter"
	if strings.Join(got, ",") != want {
		t.Errorf("applyResumeOverrides = %v, want %s", got, want)
	}
}

func TestStripLaunchPort(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"bare flag", []string{"glm-5.1", "--port", "8787"}, []string{"glm-5.1"}},
		{"equals form", []string{"glm-5.1", "--port=8787", "--worker", "w"}, []string{"glm-5.1", "--worker", "w"}},
		{"no port", []string{"glm-5.1", "--worker", "w"}, []string{"glm-5.1", "--worker", "w"}},
		{"port at end", []string{"glm-5.1", "--port"}, []string{"glm-5.1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripLaunchPort(tc.in)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("stripLaunchPort(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestReplayLegacyTUISessionRestoresProvider(t *testing.T) {
	rec := session.Record{
		SessionID:   "072e63a1-819a-4682-a742-559695c3cd76",
		Provider:    "modelscope",
		Model:       "zai-org/GLM-5.2",
		WorkerModel: "worker",
		Port:        8787,
	}
	got, err := replayLaunchArgs(rec)
	if err != nil {
		t.Fatal(err)
	}
	// The recorded --port is deliberately NOT replayed: a resume always binds a
	// fresh OS-assigned free port so it can never collide with a still-live
	// session launched with an explicit --port.
	want := "zai-org/GLM-5.2,--provider,modelscope,--worker,worker"
	if strings.Join(got, ",") != want {
		t.Fatalf("replayLaunchArgs = %v, want %s", got, want)
	}
}

func TestBuildResumeOptionNilWithoutARecordedSession(t *testing.T) {
	restoreHome := setTempHome(t)
	defer restoreHome()
	restoreWD := setTempWorkDir(t)
	defer restoreWD()

	if opt := buildResumeOption(); opt != nil {
		t.Errorf("buildResumeOption = %+v, want nil with nothing recorded", opt)
	}
}

func TestBuildResumeOptionDescribesARecordedSession(t *testing.T) {
	restoreHome := setTempHome(t)
	defer restoreHome()
	workDir := setTempWorkDir(t)
	defer workDir()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rec := session.Record{
		SessionID: "072e63a1-819a-4682-a742-559695c3cd76",
		WorkDir:   cwd,
		Model:     "glm-5.1",
	}
	if err := session.Save(sessionCacheDir(), rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	opt := buildResumeOption()
	if opt == nil {
		t.Fatal("buildResumeOption = nil, want a resume option")
	}
	if opt.SessionID != rec.SessionID || opt.Label != "glm-5.1" {
		t.Errorf("buildResumeOption = %+v", opt)
	}
}

// setTempHome points HOME (so sessionCacheDir resolves under a scratch dir)
// at a fresh temp dir and returns a func restoring the original value.
func setTempHome(t *testing.T) func() {
	t.Helper()
	old := os.Getenv("HOME")
	if err := os.Setenv("HOME", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return func() { os.Setenv("HOME", old) }
}

// setTempWorkDir chdirs into a fresh temp dir (so os.Getwd() in the code
// under test is deterministic) and returns a func restoring the original cwd.
func setTempWorkDir(t *testing.T) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return func() { os.Chdir(old) }
}

func TestApplyResumeOverridesAppendsWhenAbsent(t *testing.T) {
	recorded := []string{"glm-5.1"}
	got := applyResumeOverrides(recorded, []string{"--worker", "kimi"})
	want := "glm-5.1,--worker,kimi"
	if strings.Join(got, ",") != want {
		t.Errorf("applyResumeOverrides = %v, want %s", got, want)
	}
}
