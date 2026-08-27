// Session resume: ultra-zen mints a Claude Code session ID at launch (or
// reuses one being resumed), records the exact invocation that produced it,
// and can later reopen that session with `ultra-zen resume`. This is a port
// of ggrun's `ggrun claude resume` feature (go/cmd/ggrun/claude_resume.go)
// adapted to ultra-zen's simpler launch shape.
//
// ggrun also refuses a resume whose backend slot has shrunk below the
// recorded conversation's size, because a local KV cache can silently
// truncate or reinterpret data built under a larger context. ultra-zen holds
// no local backend state at all — it is a stateless HTTP proxy — so that
// failure mode does not exist here and no such guard is needed.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/raketenkater/ultra-zen/internal/session"
	"github.com/raketenkater/ultra-zen/internal/tui"
)

// sessionSpec carries the resume decision from launch into the client
// invocation.
type sessionSpec struct {
	ID       string
	Resume   bool
	Workflow *session.Workflow
	Cached   int
}

// claudeProjectsDir is where Claude Code keeps per-project session state.
func claudeProjectsDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// sessionCacheDir is where ultra-zen records resumable sessions.
func sessionCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "ultra-zen")
}

// newSessionSpec mints a session ID for a fresh launch.
func newSessionSpec() (*sessionSpec, error) {
	id, err := session.NewSessionID()
	if err != nil {
		return nil, err
	}
	return &sessionSpec{ID: id}, nil
}

// resolveSessionTarget loads a recorded session for --resume-session. The
// value is a session ID, or "latest"/"last" for the newest session recorded
// for this working directory.
func resolveSessionTarget(cacheDir, workDir, value string) (session.Record, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "latest") || strings.EqualFold(value, "last") {
		return session.Latest(cacheDir, workDir)
	}
	return session.Load(cacheDir, value)
}

// buildResumeOption checks for a session recorded for the current directory
// and, if one exists, describes it for the TUI's opening screen. Returns nil
// when there is nothing to resume, so the picker shows no extra row.
func buildResumeOption() *tui.ResumeOption {
	workDir, err := os.Getwd()
	if err != nil {
		return nil
	}
	rec, err := session.Latest(sessionCacheDir(), workDir)
	if err != nil {
		return nil
	}
	label := rec.Model
	if label == "" {
		label = rec.SessionID
	}
	desc := rec.Recorded.Local().Format("2006-01-02 15:04")
	if wf, cached := session.LatestRun(claudeProjectsDir(), rec.WorkDir, rec.SessionID); wf != nil {
		desc = fmt.Sprintf("%s · workflow %s: %d agents cached", desc, wf.RunID, cached)
	}
	return &tui.ResumeOption{SessionID: rec.SessionID, Label: label, Description: desc}
}

// sessionSpecFromRecord turns a recorded session into a launch spec,
// discovering whatever workflow run Claude Code's own transcript state has
// for it.
func sessionSpecFromRecord(rec session.Record) *sessionSpec {
	spec := &sessionSpec{ID: rec.SessionID, Resume: true}
	if wf, cached := session.LatestRun(claudeProjectsDir(), rec.WorkDir, rec.SessionID); wf != nil {
		spec.Workflow, spec.Cached = wf, cached
	}
	return spec
}

// recordSession stores the session and the exact shape it ran under.
// Recording is evidence, not a dependency: a failure must not stop a launch.
func recordSession(cacheDir string, spec *sessionSpec, provider, model, workerModel, fastModel string, port int, launchArgs []string) {
	if spec == nil || cacheDir == "" {
		return
	}
	workDir, err := os.Getwd()
	if err != nil {
		return
	}
	rec := session.Record{
		SessionID:   spec.ID,
		WorkDir:     workDir,
		Provider:    provider,
		Model:       model,
		WorkerModel: workerModel,
		FastModel:   fastModel,
		Port:        port,
		LaunchArgs:  launchArgs,
		Workflow:    spec.Workflow,
	}
	if err := session.Save(cacheDir, rec); err != nil {
		fmt.Fprintf(os.Stderr, "[ultra-zen] could not record session for resume: %v\n", err)
	}
}

// resolveLaunchSession resolves the session for this launch: either a
// recorded one being resumed (resumeValue set), or a freshly minted ID that
// is recorded for next time.
func resolveLaunchSession(cacheDir, resumeValue, provider, model, workerModel, fastModel string, port int, launchArgs []string) (*sessionSpec, error) {
	if resumeValue != "" {
		workDir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		rec, err := resolveSessionTarget(cacheDir, workDir, resumeValue)
		if err != nil {
			return nil, err
		}
		return sessionSpecFromRecord(rec), nil
	}
	spec, err := newSessionSpec()
	if err != nil {
		// A missing session ID only costs the ability to resume later, so it
		// must not block the launch itself.
		fmt.Fprintf(os.Stderr, "[ultra-zen] could not mint a session id: %v\n", err)
		return nil, nil
	}
	recordSession(cacheDir, spec, provider, model, workerModel, fastModel, port, launchArgs)
	return spec, nil
}

// sessionClaudeArgs prepends the session flags to the client invocation.
//
// A resume must reuse the original session ID. --fork-session mints a new
// one, which moves the journal path and silently discards every cached
// agent, so it is refused rather than quietly honoured.
func sessionClaudeArgs(spec *sessionSpec, userArgs, args []string) ([]string, error) {
	if spec == nil || spec.ID == "" {
		return args, nil
	}
	if hasArg(userArgs, "--session-id") || hasArg(userArgs, "--resume") || hasArg(userArgs, "-r") {
		// The user pinned their own session; do not fight them for it.
		return args, nil
	}
	if !spec.Resume {
		return append([]string{"--session-id", spec.ID}, args...), nil
	}
	if hasArg(userArgs, "--fork-session") {
		return nil, fmt.Errorf(
			"--fork-session cannot be combined with a session resume: forking mints a new " +
				"session ID, which moves the workflow journal path and discards every cached agent")
	}
	return append([]string{"--resume", spec.ID}, args...), nil
}

// sessionResumePrompt asks Claude Code to continue the recorded workflow
// from its journal. Cached agents replay without a model call; anything
// still in flight when the session stopped re-runs. It is appended as the
// last CLI arg so it becomes Claude's opening turn.
func sessionResumePrompt(spec *sessionSpec) string {
	if spec == nil || spec.Workflow == nil || spec.Workflow.RunID == "" {
		return ""
	}
	wf := spec.Workflow
	var b strings.Builder
	fmt.Fprintf(&b, "Resume the interrupted workflow run %s.", wf.RunID)
	if wf.ScriptPath != "" {
		fmt.Fprintf(&b, " Call Workflow({scriptPath: %q, resumeFromRunId: %q}).", wf.ScriptPath, wf.RunID)
	} else {
		fmt.Fprintf(&b, " Call Workflow with resumeFromRunId: %q and the same script and args as before.", wf.RunID)
	}
	b.WriteString(" Do not change the script or args: agents whose prompt and options are unchanged replay from cache," +
		" and any edit re-runs everything after the first changed call.")
	return b.String()
}

// describeSessionResume reports what a resume will actually recover, so the
// cost is visible before the proxy and claude client start.
func describeSessionResume(spec *sessionSpec) string {
	if spec == nil || !spec.Resume {
		return ""
	}
	if spec.Workflow == nil || spec.Workflow.RunID == "" {
		return fmt.Sprintf("[ultra-zen] Resuming session %s (no recorded workflow run).", spec.ID)
	}
	return fmt.Sprintf(
		"[ultra-zen] Resuming session %s, workflow %s: %d completed agents replay from cache; "+
			"agents still running when it stopped re-run.",
		spec.ID, spec.Workflow.RunID, spec.Cached)
}

// refreshSessionRecord re-records the session once Claude Code exits, so a
// workflow started during the session is part of the resume handle. The run
// ID is assigned inside Claude Code and cannot be known at launch.
func refreshSessionRecord(cacheDir string, spec *sessionSpec, provider, model, workerModel, fastModel string, port int, launchArgs []string) {
	if spec == nil || spec.ID == "" || cacheDir == "" {
		return
	}
	workDir, err := os.Getwd()
	if err != nil {
		return
	}
	if wf, cached := session.LatestRun(claudeProjectsDir(), workDir, spec.ID); wf != nil {
		spec.Workflow, spec.Cached = wf, cached
		fmt.Printf("[ultra-zen] Session %s recorded: workflow %s has %d completed agents cached. "+
			"Resume with: ultra-zen resume\n", spec.ID, wf.RunID, cached)
	}
	if spec.Resume {
		// A resumed session keeps its original recorded shape; only the
		// workflow pointer is refreshed above.
		if rec, err := session.Load(cacheDir, spec.ID); err == nil {
			rec.Workflow = spec.Workflow
			if saveErr := session.Save(cacheDir, rec); saveErr != nil {
				fmt.Fprintf(os.Stderr, "[ultra-zen] could not update session record: %v\n", saveErr)
			}
			return
		}
	}
	recordSession(cacheDir, spec, provider, model, workerModel, fastModel, port, launchArgs)
}

// stripResumeArgs removes the resume flag from a recorded launch so
// replaying it cannot chain a resume of a resume.
func stripResumeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--resume-session":
			i++ // also drop its value
		case strings.HasPrefix(args[i], "--resume-session="):
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// hasArg reports whether args contains the given flag (as a bare flag or
// --flag=value).
func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// cmdSessions implements `ultra-zen sessions` (list) and `ultra-zen resume`.
func cmdSessions(sub string, args []string) {
	switch sub {
	case "sessions":
		cmdSessionsList()
	case "resume":
		target, overrides := parseSessionResumeArgs(args)
		cmdSessionResume(target, overrides)
	}
}

// parseSessionResumeArgs splits `ultra-zen resume` arguments into the target
// session and any override flags applied on top of the recorded launch.
func parseSessionResumeArgs(args []string) (target string, overrides []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			overrides = append(overrides, arg)
			// ultra-zen spells valued flags "--flag value", so a following
			// token that is not itself a flag belongs to this one.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				overrides = append(overrides, args[i+1])
				i++
			}
			continue
		}
		if target == "" {
			target = arg
		}
	}
	return target, overrides
}

// applyResumeOverrides layers override flags onto a recorded launch,
// replacing a flag's value where it was already set and appending it
// otherwise.
func applyResumeOverrides(recorded, overrides []string) []string {
	if len(overrides) == 0 {
		return recorded
	}
	out := append([]string(nil), recorded...)
	for i := 0; i < len(overrides); i++ {
		flag := overrides[i]
		value := ""
		hasValue := false
		if i+1 < len(overrides) && !strings.HasPrefix(overrides[i+1], "-") {
			value, hasValue = overrides[i+1], true
			i++
		}
		at := -1
		for j, tok := range out {
			if tok == flag {
				at = j
				break
			}
		}
		switch {
		case at < 0 && hasValue:
			out = append(out, flag, value)
		case at < 0:
			out = append(out, flag)
		case !hasValue:
			// A bare flag already present needs nothing.
		case at+1 < len(out) && !strings.HasPrefix(out[at+1], "-"):
			out[at+1] = value
		default:
			// Recorded as a bare flag but overridden with a value.
			rest := append([]string{value}, out[at+1:]...)
			out = append(out[:at+1], rest...)
		}
	}
	return out
}

// stripLaunchPort removes --port N / --port=N from a recorded launch so a
// resume always binds a fresh free port. Replaying an explicit --port onto a
// still-live session would fail with "address already in use" and kill the
// resume; the default is port 0 (OS-assigned) which can never collide.
func stripLaunchPort(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--port" && i+1 < len(args):
			i++ // also drop the value
		case args[i] == "--port":
		case strings.HasPrefix(args[i], "--port="):
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// replayLaunchArgs returns the exact recorded command shape. Older TUI
// records have an empty LaunchArgs field, so reconstruct at least the model,
// provider, worker and port from their structured fields.
func replayLaunchArgs(rec session.Record) ([]string, error) {
	launchArgs := stripLaunchPort(stripResumeArgs(rec.LaunchArgs))
	if len(launchArgs) > 0 {
		return launchArgs, nil
	}
	if rec.Model == "" {
		return nil, fmt.Errorf("session %s has no recorded launch to reproduce", rec.SessionID)
	}
	launchArgs = []string{rec.Model}
	if rec.Provider != "" {
		launchArgs = append(launchArgs, "--provider", rec.Provider)
	}
	if rec.WorkerModel != "" {
		launchArgs = append(launchArgs, "--worker", rec.WorkerModel)
	}
	if rec.FastModel != "" {
		launchArgs = append(launchArgs, "--fast-model", rec.FastModel)
	}
	// The recorded --port is deliberately not replayed: resume should always
	// bind a fresh OS-assigned free port so it can never collide with a
	// still-live session that was launched with an explicit --port.
	return launchArgs, nil
}

func cmdSessionsList() {
	cacheDir := sessionCacheDir()
	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	records, err := session.List(cacheDir, workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(records) == 0 {
		fmt.Printf("No recorded ultra-zen sessions for %s\n", workDir)
		return
	}
	fmt.Printf("Recorded ultra-zen sessions for %s:\n\n", workDir)
	for _, rec := range records {
		wf, cached := session.LatestRun(claudeProjectsDir(), rec.WorkDir, rec.SessionID)
		fmt.Printf("  %s  %s  %s\n", rec.SessionID,
			rec.Recorded.Local().Format("2006-01-02 15:04"), rec.Model)
		if wf != nil {
			fmt.Printf("      workflow %s (%s): %d completed agents cached\n", wf.RunID, wf.Name, cached)
		}
	}
	fmt.Println("\nResume the newest with: ultra-zen resume")
}

// cmdSessionResume replays the recorded launch argv and re-execs ultra-zen
// with --resume-session appended, rather than re-deriving flags that may
// since have changed.
func cmdSessionResume(target string, overrides []string) {
	cacheDir := sessionCacheDir()
	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	rec, err := resolveSessionTarget(cacheDir, workDir, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "List recorded sessions with: ultra-zen sessions")
		os.Exit(1)
	}
	if wf, cached := session.LatestRun(claudeProjectsDir(), rec.WorkDir, rec.SessionID); wf != nil {
		fmt.Printf("[ultra-zen] Session %s, workflow %s (%s): %d completed agents will replay from cache.\n",
			rec.SessionID, wf.RunID, wf.Name, cached)
	}
	launchArgs, err := replayLaunchArgs(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(overrides) > 0 {
		launchArgs = applyResumeOverrides(launchArgs, overrides)
		fmt.Printf("[ultra-zen] Overriding recorded launch: %s\n", strings.Join(overrides, " "))
	}
	launchArgs = append(launchArgs, "--resume-session", rec.SessionID)

	exe, err := os.Executable()
	if err != nil {
		exe = "ultra-zen"
	}
	cmd := exec.Command(exe, launchArgs...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
