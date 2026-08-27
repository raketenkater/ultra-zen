// Package session records which Claude Code session an ultra-zen launch
// produced, so a later `ultra-zen resume` can reopen the same conversation
// and continue an interrupted Ultracode workflow run.
//
// Claude Code's workflow resume cache lives at
//
//	<sessionProjectDir>/<sessionID>/subagents/workflows/<runID>/journal.jsonl
//
// so the session ID is the handle for everything already computed. Unlike
// ggrun, ultra-zen holds no local backend state (no KV cache, no GPU
// placement) that a later launch could reinterpret, so there is nothing to
// validate before a resume: the recorded launch args are simply replayed.
package session

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DirName is the subdirectory of the ultra-zen cache holding session records.
const DirName = "claude-sessions"

// Workflow identifies a workflow run inside a session so a resume can
// continue it instead of only reopening the conversation.
type Workflow struct {
	RunID      string `json:"run_id,omitempty"`
	Name       string `json:"name,omitempty"`
	ScriptPath string `json:"script_path,omitempty"`
}

// Record is one resumable Claude Code session.
type Record struct {
	SessionID   string    `json:"session_id"`
	Recorded    time.Time `json:"recorded"`
	WorkDir     string    `json:"work_dir"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	WorkerModel string    `json:"worker_model,omitempty"`
	FastModel   string    `json:"fast_model,omitempty"`
	Port        int       `json:"port,omitempty"`
	LaunchArgs  []string  `json:"launch_args,omitempty"`
	Workflow    *Workflow `json:"workflow,omitempty"`
}

// NewSessionID returns a random RFC 4122 version 4 UUID. Claude Code's
// --session-id requires a valid UUID, so ultra-zen mints one at launch
// instead of discovering it afterwards by scanning transcripts.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ValidSessionID reports whether id is a UUID in the form Claude Code
// accepts. It also keeps a hostile id from escaping the records directory.
func ValidSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// Dir returns the records directory inside the given ultra-zen cache directory.
func Dir(cacheDir string) string {
	return filepath.Join(cacheDir, DirName)
}

// Save writes one record, replacing any earlier record for the same session.
func Save(cacheDir string, rec Record) error {
	if !ValidSessionID(rec.SessionID) {
		return fmt.Errorf("invalid session id %q", rec.SessionID)
	}
	dir := Dir(cacheDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if rec.Recorded.IsZero() {
		rec.Recorded = time.Now().UTC()
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session record: %w", err)
	}
	path := filepath.Join(dir, rec.SessionID+".json")
	// Write and rename so an interrupted save cannot leave a half record that
	// would later be resumed into.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// Load reads one session record by ID.
func Load(cacheDir, sessionID string) (Record, error) {
	var rec Record
	if !ValidSessionID(sessionID) {
		return rec, fmt.Errorf("invalid session id %q", sessionID)
	}
	path := filepath.Join(Dir(cacheDir), sessionID+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return rec, fmt.Errorf("no recorded session %s: %w", sessionID, err)
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		return rec, fmt.Errorf("decode %s: %w", path, err)
	}
	return rec, nil
}

// List returns records for workDir, newest first. An empty workDir lists all.
func List(cacheDir, workDir string) ([]Record, error) {
	entries, err := os.ReadDir(Dir(cacheDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Record
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		rec, err := Load(cacheDir, strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		if workDir != "" && rec.WorkDir != workDir {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Recorded.After(out[j].Recorded) })
	return out, nil
}

// Latest returns the newest record for workDir.
func Latest(cacheDir, workDir string) (Record, error) {
	records, err := List(cacheDir, workDir)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, fmt.Errorf("no recorded ultra-zen session for %s", workDir)
	}
	return records[0], nil
}

// JournalPath returns where Claude Code keeps a session's workflow resume
// cache, so a caller can report whether there is anything to recover before
// paying for a relaunch. It mirrors the client's own path construction:
// <projects>/<projectKey>/<sessionID>/subagents/workflows/<runID>/journal.jsonl
func JournalPath(projectsDir, workDir, sessionID, runID string) string {
	return filepath.Join(projectsDir, ProjectKey(workDir), sessionID,
		"subagents", "workflows", runID, "journal.jsonl")
}

// LatestRun finds the newest workflow run in a session and how much of it is
// recoverable. The run ID is assigned inside Claude Code, so ultra-zen
// discovers it from the session directory rather than trying to observe it
// at launch.
//
// The returned Workflow is nil when the session ran no workflow.
func LatestRun(projectsDir, workDir, sessionID string) (*Workflow, int) {
	sessionDir := filepath.Join(projectsDir, ProjectKey(workDir), sessionID)
	entries, err := os.ReadDir(filepath.Join(sessionDir, "subagents", "workflows"))
	if err != nil {
		return nil, 0
	}
	var (
		bestRun  string
		bestMod  time.Time
		bestSize int
	)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		journal := filepath.Join(sessionDir, "subagents", "workflows", entry.Name(), "journal.jsonl")
		info, err := os.Stat(journal)
		if err != nil {
			continue
		}
		// Prefer the most recently written run; a run with no cached result is
		// not worth resuming into.
		cached := CachedAgents(journal)
		if cached == 0 {
			continue
		}
		if bestRun == "" || info.ModTime().After(bestMod) {
			bestRun, bestMod, bestSize = entry.Name(), info.ModTime(), cached
		}
	}
	if bestRun == "" {
		return nil, 0
	}
	wf := &Workflow{RunID: bestRun}
	// The run's definition carries the script path and name. It is written
	// when a run finishes, so treat it as optional rather than required.
	if body, err := os.ReadFile(filepath.Join(sessionDir, "workflows", bestRun+".json")); err == nil {
		var def struct {
			ScriptPath   string `json:"scriptPath"`
			WorkflowName string `json:"workflowName"`
		}
		if json.Unmarshal(body, &def) == nil {
			wf.ScriptPath, wf.Name = def.ScriptPath, def.WorkflowName
		}
	}
	return wf, bestSize
}

// CachedAgents counts completed agent results in a journal. Resuming replays
// exactly these; anything that was still in flight re-runs from zero.
func CachedAgents(journalPath string) int {
	body, err := os.ReadFile(journalPath)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Type == "result" {
			count++
		}
	}
	return count
}

// ProjectKey mirrors Claude Code's per-project directory naming: the
// absolute path with separators replaced by dashes.
func ProjectKey(workDir string) string {
	key := filepath.ToSlash(workDir)
	key = strings.ReplaceAll(key, "/", "-")
	key = strings.ReplaceAll(key, ".", "-")
	return key
}
