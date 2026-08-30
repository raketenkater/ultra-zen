package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewSessionIDIsAValidUUIDAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if !ValidSessionID(id) {
			t.Fatalf("generated id %q is not a valid session id", id)
		}
		// Claude Code requires a v4 UUID; a non-conforming id is rejected at
		// launch, which would silently lose the resume handle.
		if id[14] != '4' {
			t.Fatalf("id %q is not version 4", id)
		}
		if variant := id[19]; variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
			t.Fatalf("id %q has wrong variant nibble %q", id, variant)
		}
		if seen[id] {
			t.Fatalf("duplicate session id %q", id)
		}
		seen[id] = true
	}
}

func TestValidSessionIDRejectsPathEscapes(t *testing.T) {
	for _, id := range []string{
		"", "not-a-uuid", "../../etc/passwd",
		"../../../home/mik/.claude/creds.json",
		"12345678-1234-1234-1234-12345678901", // too short
		"12345678-1234-1234-1234-1234567890123",
		"12345678/1234-1234-1234-123456789012",
		"gggggggg-1234-1234-1234-123456789012",
	} {
		if ValidSessionID(id) {
			t.Errorf("ValidSessionID(%q) = true, want false", id)
		}
	}
	if !ValidSessionID("072e63a1-819a-4682-a742-559695c3cd76") {
		t.Error("rejected a real session id")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec := Record{
		SessionID:  "072e63a1-819a-4682-a742-559695c3cd76",
		WorkDir:    "/home/mik/ultra-zen",
		Provider:   "opencode-go",
		Model:      "glm-5.1",
		Port:       8081,
		LaunchArgs: []string{"glm-5.1", "--worker", "mini-max-m2.5"},
		Workflow:   &Workflow{RunID: "wf_894b5285-5d3", Name: "deep-research"},
	}
	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(dir, rec.SessionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Model != rec.Model || got.Port != rec.Port {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if got.Workflow == nil || got.Workflow.RunID != "wf_894b5285-5d3" {
		t.Errorf("workflow not preserved: %+v", got.Workflow)
	}
	if got.Recorded.IsZero() {
		t.Error("Recorded not stamped on save")
	}
	// A partial write must not survive as a loadable record.
	if entries, _ := os.ReadDir(Dir(dir)); len(entries) != 1 {
		t.Errorf("want 1 record file, got %d", len(entries))
	}
}

func TestSaveRejectsInvalidSessionID(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Record{SessionID: "../escape"}); err == nil {
		t.Fatal("Save accepted an invalid session id")
	}
	if _, err := os.Stat(Dir(dir)); err == nil {
		t.Error("Save created the records directory for an invalid id")
	}
}

func TestListAndLatestAreScopedToWorkDirAndOrdered(t *testing.T) {
	dir := t.TempDir()
	older := Record{
		SessionID: "11111111-1111-4111-8111-111111111111",
		WorkDir:   "/project/a", Recorded: time.Now().Add(-2 * time.Hour),
	}
	newer := Record{
		SessionID: "22222222-2222-4222-8222-222222222222",
		WorkDir:   "/project/a", Recorded: time.Now().Add(-1 * time.Hour),
	}
	other := Record{
		SessionID: "33333333-3333-4333-8333-333333333333",
		WorkDir:   "/project/b", Recorded: time.Now(),
	}
	for _, rec := range []Record{older, newer, other} {
		if err := Save(dir, rec); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// An empty projects dir disables the transcript check: every record
	// counts as resumable, which is this test's concern.
	list, err := List(dir, "/project/a", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 records for /project/a, got %d", len(list))
	}
	if list[0].SessionID != newer.SessionID {
		t.Errorf("List not newest-first: got %s", list[0].SessionID)
	}
	if !list[0].Resumable || !list[1].Resumable {
		t.Errorf("List left records unresumable without a projects dir: %+v", list)
	}
	latest, err := Latest(dir, "/project/a", "")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.SessionID != newer.SessionID {
		t.Errorf("Latest = %s, want %s", latest.SessionID, newer.SessionID)
	}
	// A different project must not resume into this one's session.
	if _, err := Latest(dir, "/project/c", ""); err == nil {
		t.Error("Latest returned a record for an unrelated work dir")
	}
}

func TestListOnMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	list, err := List(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("List on empty cache: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want no records, got %d", len(list))
	}
}

// writeTranscript creates the Claude Code transcript file for rec under a
// temp projects root, so resumability can be judged for real.
func writeTranscript(t *testing.T, projectsDir string, rec Record) {
	t.Helper()
	path := TranscriptPath(projectsDir, rec)
	if path == "" {
		t.Fatalf("TranscriptPath empty for %+v", rec)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"type\":\"user\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptPathMatchesClaudeCodeLayout(t *testing.T) {
	rec := Record{SessionID: "072e63a1-819a-4682-a742-559695c3cd76", WorkDir: "/home/mik/ultra-zen"}
	want := filepath.Join("/claude/projects", "-home-mik-ultra-zen", rec.SessionID+".jsonl")
	if got := TranscriptPath("/claude/projects", rec); got != want {
		t.Errorf("TranscriptPath = %q, want %q", got, want)
	}
	for _, tc := range []Record{
		{SessionID: rec.SessionID},
		{SessionID: rec.SessionID, WorkDir: ""},
	} {
		if got := TranscriptPath("/claude/projects", tc); got != "" {
			t.Errorf("TranscriptPath(%+v) = %q, want empty", tc, got)
		}
	}
}

func TestHasTranscriptFailsOpenWithoutProjectsDir(t *testing.T) {
	rec := Record{SessionID: "072e63a1-819a-4682-a742-559695c3cd76", WorkDir: "/project/a"}
	if !HasTranscript("", rec) {
		t.Error("HasTranscript without a projects dir must fail open (true)")
	}
}

func TestListMarksResumableAgainstTheTranscript(t *testing.T) {
	dir := t.TempDir()
	projects := t.TempDir()
	live := Record{
		SessionID: "11111111-1111-4111-8111-111111111111",
		WorkDir:   "/project/a", Recorded: time.Now().Add(-2 * time.Hour),
	}
	dead := Record{
		SessionID: "22222222-2222-4222-8222-222222222222",
		WorkDir:   "/project/a", Recorded: time.Now().Add(-1 * time.Hour),
	}
	writeTranscript(t, projects, live)
	for _, rec := range []Record{dead, live} {
		if err := Save(dir, rec); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	list, err := List(dir, "/project/a", projects)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 records, got %d", len(list))
	}
	// Newest first: the transcript-less launch is newer but must be marked
	// dead, not hidden.
	if list[0].SessionID != dead.SessionID || list[0].Resumable {
		t.Errorf("newest record should be dead, got %+v", list[0])
	}
	if list[1].SessionID != live.SessionID || !list[1].Resumable {
		t.Errorf("older record with a transcript should be resumable, got %+v", list[1])
	}
	latest, err := Latest(dir, "/project/a", projects)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.SessionID != live.SessionID {
		t.Errorf("Latest = %s, want the live %s (not the newer corpse %s)",
			latest.SessionID, live.SessionID, dead.SessionID)
	}
}

func TestLatestErrorsWhenEveryRecordIsDead(t *testing.T) {
	dir := t.TempDir()
	projects := t.TempDir()
	dead := Record{
		SessionID: "44444444-4444-4444-8444-444444444444",
		WorkDir:   "/project/a", Recorded: time.Now(),
	}
	if err := Save(dir, dead); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rec, err := Latest(dir, "/project/a", projects)
	if err == nil {
		t.Fatalf("Latest returned a corpse: %+v", rec)
	}
	if !strings.Contains(err.Error(), "no resumable") {
		t.Errorf("error should say no resumable session: %v", err)
	}
}

func TestListPrunesStaleDeadRecordsOnly(t *testing.T) {
	dir := t.TempDir()
	projects := t.TempDir()
	staleDead := Record{
		SessionID: "11111111-1111-4111-8111-111111111111",
		WorkDir:   "/project/a", Recorded: time.Now().Add(-8 * 24 * time.Hour),
	}
	freshDead := Record{
		SessionID: "22222222-2222-4222-8222-222222222222",
		WorkDir:   "/project/a", Recorded: time.Now().Add(-1 * time.Hour),
	}
	staleLive := Record{
		SessionID: "33333333-3333-4333-8333-333333333333",
		WorkDir:   "/project/a", Recorded: time.Now().Add(-8 * 24 * time.Hour),
	}
	writeTranscript(t, projects, staleLive)
	for _, rec := range []Record{staleDead, freshDead, staleLive} {
		if err := Save(dir, rec); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	list, err := List(dir, "/project/a", projects)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want stale-dead pruned, fresh-dead and stale-live kept; got %d records", len(list))
	}
	if _, err := os.Stat(filepath.Join(Dir(dir), staleDead.SessionID+".json")); !os.IsNotExist(err) {
		t.Errorf("stale dead record still on disk (stat err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(dir), freshDead.SessionID+".json")); err != nil {
		t.Errorf("fresh dead record must survive the TTL window: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(dir), staleLive.SessionID+".json")); err != nil {
		t.Errorf("live record must never be pruned: %v", err)
	}
	// A workDir-filtered listing must not prune other projects' records that
	// share the same cache dir: /project/b's stale corpse survives when
	// /project/a is listed.
	foreign := Record{
		SessionID: "44444444-4444-4444-8444-444444444444",
		WorkDir:   "/project/b", Recorded: time.Now().Add(-30 * 24 * time.Hour),
	}
	if err := Save(dir, foreign); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := List(dir, "/project/a", projects); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(dir), foreign.SessionID+".json")); err != nil {
		t.Errorf("a filtered listing pruned another project's record: %v", err)
	}
	// While an unfiltered listing of the same cache does prune it.
	if _, err := List(dir, "", projects); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(dir), foreign.SessionID+".json")); !os.IsNotExist(err) {
		t.Errorf("unfiltered listing should have pruned the stale foreign corpse (stat err %v)", err)
	}
}

func TestProjectKeyMatchesClaudeCodeLayout(t *testing.T) {
	cases := map[string]string{
		"/home/mik/ultra-zen":             "-home-mik-ultra-zen",
		"/home/mik/ggrun/.src/llm-server": "-home-mik-ggrun--src-llm-server",
	}
	for in, want := range cases {
		if got := ProjectKey(in); got != want {
			t.Errorf("ProjectKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJournalPathAndCachedAgents(t *testing.T) {
	projects := t.TempDir()
	workDir := "/home/mik/ultra-zen"
	sessionID := "072e63a1-819a-4682-a742-559695c3cd76"
	run := "wf_894b5285-5d3"
	path := JournalPath(projects, workDir, sessionID, run)
	want := filepath.Join(projects, "-home-mik-ultra-zen", sessionID,
		"subagents", "workflows", run, "journal.jsonl")
	if path != want {
		t.Fatalf("JournalPath = %q, want %q", path, want)
	}

	if got := CachedAgents(path); got != 0 {
		t.Errorf("missing journal reported %d cached agents, want 0", got)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	journal := `{"type":"started","key":"a","agentId":"1"}
{"type":"result","key":"a","agentId":"1","result":{}}
{"type":"started","key":"b","agentId":"2"}
{"type":"result","key":"b","agentId":"2","result":{}}
{"type":"started","key":"c","agentId":"3"}
`
	if err := os.WriteFile(path, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	// Two results and three starts: only the finished agents replay.
	if got := CachedAgents(path); got != 2 {
		t.Errorf("CachedAgents = %d, want 2", got)
	}
}

func TestLatestRunPrefersNewestRunWithCachedAgents(t *testing.T) {
	projects := t.TempDir()
	workDir := "/home/mik/ultra-zen"
	sessionID := "072e63a1-819a-4682-a742-559695c3cd76"

	empty := JournalPath(projects, workDir, sessionID, "wf_empty")
	if err := os.MkdirAll(filepath.Dir(empty), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, []byte(`{"type":"started","key":"a","agentId":"1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	populated := JournalPath(projects, workDir, sessionID, "wf_populated")
	if err := os.MkdirAll(filepath.Dir(populated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(populated, []byte(`{"type":"result","key":"a","agentId":"1","result":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	wf, cached := LatestRun(projects, workDir, sessionID)
	if wf == nil || wf.RunID != "wf_populated" {
		t.Fatalf("LatestRun = %+v, want wf_populated", wf)
	}
	if cached != 1 {
		t.Errorf("cached = %d, want 1", cached)
	}
}

func TestLatestRunIsNilWhenNoRunHasCachedAgents(t *testing.T) {
	projects := t.TempDir()
	workDir := "/home/mik/ultra-zen"
	sessionID := "072e63a1-819a-4682-a742-559695c3cd76"
	if wf, cached := LatestRun(projects, workDir, sessionID); wf != nil || cached != 0 {
		t.Errorf("LatestRun on unknown session = %+v, %d, want nil, 0", wf, cached)
	}
}
