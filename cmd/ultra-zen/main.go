// Command ultra-zen launches Claude Code backed by an opencode Zen model.
//
// It reads the opencode-go API key from opencode's auth store, lists the
// models available on the Zen gateway, lets you pick one, starts a local
// Anthropic->OpenAI translation proxy, and execs `claude` pointed at it.
//
// Usage:
//
//	ultra-zen                 # interactive model selector, then launch claude
//	ultra-zen <model>         # skip the selector, use this model id
//	ultra-zen <model> -- <args>   # pass args through to claude
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/raketenkater/ultra-zen/internal/auth"
	"github.com/raketenkater/ultra-zen/internal/claude"
	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/proxy"
	"github.com/raketenkater/ultra-zen/internal/tui"
	"github.com/raketenkater/ultra-zen/internal/workflow"
)

// Version is set at build time via -ldflags. Falls back to "dev" in local builds.
var Version = "dev"

func main() {
	// Subcommand dispatch (before flag parsing, which would choke on the
	// subcommand). `workflow-hook` is invoked by Claude Code's PreToolUse hook
	// to deterministically rewrite Workflow agent() scripts with a safe stallMs.
	if len(os.Args) > 1 && os.Args[1] == "workflow-hook" {
		workflow.RunHook()
		return
	}

	// Redirect the proxy's log output (log.Printf in internal/proxy) to a file
	// instead of stderr. Claude Code's TUI owns stderr, so any log line written
	// there leaks into the front-end as garbled text. Write to
	// ~/.cache/ultra-zen/proxy.log so diagnostics are still available.
	redirectProxyLog()

	var (
		authPath      = flag.String("auth", "", "path to opencode auth.json (default: auto)")
		provider      = flag.String("provider", "opencode-go", "backend provider: opencode-go, openrouter, or codex")
		openRouterKey = flag.String("openrouter-key", "", "OpenRouter API key (or set OPENROUTER_API_KEY)")
		codexBaseURL  = flag.String("codex-url", "", "Codex endpoint base URL (or set CODEX_BASE_URL), e.g. http://127.0.0.1:8000/v1")
		codexKey      = flag.String("codex-key", "", "Codex endpoint API key (or set CODEX_API_KEY)")
		workerModel   = flag.String("worker", "", "cheaper model for background sub-agents (orchestrator/worker split)")
		port          = flag.Int("port", 0, "local proxy listen port (0 = pick a free port per instance)")
		listOnly      = flag.Bool("list", false, "list available models and exit")
		proxyOnly     = flag.Bool("proxy-only", false, "start the proxy and block (for testing)")
		showVer       = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "ultra-zen — run Claude Code on opencode Zen or OpenRouter models")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  ultra-zen                 # pick a model, then launch claude")
		fmt.Fprintln(os.Stderr, "  ultra-zen <model>         # use this model id")
		fmt.Fprintln(os.Stderr, "  ultra-zen <model> -- <args>   # pass args through to claude")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Providers:")
		fmt.Fprintln(os.Stderr, "  --provider opencode-go   Zen gateway go + free tier (default, reads opencode auth)")
		fmt.Fprintln(os.Stderr, "  --provider openrouter    OpenRouter free models (set OPENROUTER_API_KEY)")
		fmt.Fprintln(os.Stderr, "  --provider codex         Local Codex endpoint (ChatGPT sub, e.g. ChatMock)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Orchestrator/worker split (saves quota):")
		fmt.Fprintln(os.Stderr, "  --worker <model>         Use a cheaper model for background sub-agents")
		fmt.Fprintln(os.Stderr, "")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVer {
		fmt.Printf("ultra-zen %s\n", Version)
		return
	}

	rest := flag.Args()
	var modelID string
	var claudeArgs []string
	if len(rest) > 0 {
		modelID = rest[0]
		if len(rest) > 1 {
			claudeArgs = rest[1:]
		}
	}

	// Go's flag.Parse() stops at the first positional arg, so flags placed
	// after the model name (e.g. `ultra-zen glm-5.1 --provider openrouter`)
	// land in claudeArgs instead of being parsed. Re-scan those trailing args
	// for our own flags so ordering is irrelevant.
	for i := 0; i < len(claudeArgs); {
		arg := claudeArgs[i]
		switch {
		case arg == "--provider" && i+1 < len(claudeArgs):
			*provider = claudeArgs[i+1]
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--provider="):
			*provider = strings.TrimPrefix(arg, "--provider=")
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--worker" && i+1 < len(claudeArgs):
			*workerModel = claudeArgs[i+1]
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--worker="):
			*workerModel = strings.TrimPrefix(arg, "--worker=")
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--openrouter-key" && i+1 < len(claudeArgs):
			*openRouterKey = claudeArgs[i+1]
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--openrouter-key="):
			*openRouterKey = strings.TrimPrefix(arg, "--openrouter-key=")
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--codex-url" && i+1 < len(claudeArgs):
			*codexBaseURL = claudeArgs[i+1]
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--codex-url="):
			*codexBaseURL = strings.TrimPrefix(arg, "--codex-url=")
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--codex-key" && i+1 < len(claudeArgs):
			*codexKey = claudeArgs[i+1]
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--codex-key="):
			*codexKey = strings.TrimPrefix(arg, "--codex-key=")
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--port" && i+1 < len(claudeArgs):
			*port, _ = strconv.Atoi(claudeArgs[i+1])
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--port="):
			*port, _ = strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		default:
			i++
		}
	}

	httpClient := &http.Client{Timeout: 20 * time.Second}
	var list []models.Model
	var key string

	switch *provider {
	case "openrouter":
		k := *openRouterKey
		if k == "" {
			k = os.Getenv("OPENROUTER_API_KEY")
		}
		if k == "" {
			die(fmt.Errorf("OpenRouter requires an API key: set OPENROUTER_API_KEY or pass --openrouter-key\nGet one at https://openrouter.ai/keys"))
		}
		key = k
		var err error
		list, err = models.ListOpenRouter(httpClient, key)
		if err != nil {
			die(err)
		}
	case "codex":
		base := *codexBaseURL
		if base == "" {
			base = os.Getenv("CODEX_BASE_URL")
		}
		if base == "" {
			die(fmt.Errorf("codex provider requires a base URL: set CODEX_BASE_URL or pass --codex-url\nPoint it at a local Codex endpoint (e.g. ChatMock on http://127.0.0.1:8000/v1)"))
		}
		ck := *codexKey
		if ck == "" {
			ck = os.Getenv("CODEX_API_KEY")
		}
		if ck == "" {
			ck = "codex" // ChatMock ignores the key
		}
		key = ck
		var err error
		list, err = models.ListCodex(httpClient, base, ck)
		if err != nil {
			die(fmt.Errorf("codex: %w", err))
		}
	default:
		store, err := auth.Load(*authPath)
		if err != nil {
			die(err)
		}
		key, err = auth.KeyFor(store, *provider)
		if err != nil {
			die(err)
		}
		list, err = models.List(httpClient, key)
		if err != nil {
			die(err)
		}
	}
	if len(list) == 0 {
		die(fmt.Errorf("no models available for provider %q", *provider))
	}

	if *listOnly {
		for i, m := range list {
			tag := ""
			if m.Free {
				tag = " (free)"
			}
			fmt.Printf("%2d. %-28s  %s%s\n", i+1, m.ID, m.Base, tag)
		}
		return
	}

	if modelID == "" {
		var workerPick string
		var quit bool
		modelID, workerPick, quit = tui.Run(list, *provider)
		if quit || modelID == "" {
			return
		}
		// CLI --worker flag overrides TUI pick; TUI pick fills the default.
		if *workerModel == "" && workerPick != "" {
			*workerModel = workerPick
		}
	}
	selected := models.Find(list, modelID)
	if selected == nil {
		die(fmt.Errorf("model %q not found; run `ultra-zen --list` to see available models", modelID))
	}
	models.RecordRecent(selected.ID)
	models.RecordCombo(selected.ID, *workerModel)

	// Build the model list for /v1/models (Claude Code's /model command).
	modelInfos := make([]proxy.ModelInfo, 0, len(list))
	for _, m := range list {
		modelInfos = append(modelInfos, proxy.ModelInfo{ID: m.ID, Name: m.Name})
	}

	// Start the proxy.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := proxy.New(proxy.Config{
		BaseURL:     selected.Base,
		APIKey:      key,
		Model:       selected.ID,
		WorkerModel: *workerModel,
		Port:        *port,
		Models:      modelInfos,
	})
	if err := srv.Start(ctx); err != nil {
		die(fmt.Errorf("start proxy: %w", err))
	}
	if err := waitForHealth(srv.BaseURL(), 5*time.Second); err != nil {
		cancel()
		die(err)
	}

	if *proxyOnly {
		fmt.Fprintf(os.Stderr, "ultra-zen proxy ready on %s (model=%s, upstream=%s)\n", srv.BaseURL(), selected.ID, selected.Base)
		<-ctx.Done()
		return
	}

	fmt.Fprintf(os.Stderr, "\n  ultra-zen ▸ %s  (%s)\n", selected.ID, selected.Base)
	if *workerModel != "" {
		fmt.Fprintf(os.Stderr, "  worker    ▸ %s\n", *workerModel)
	}
	fmt.Fprintf(os.Stderr, "  proxy on %s  →  claude\n\n", srv.BaseURL())

	// Forward SIGINT/SIGTERM to claude and tear down the proxy.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	env := claude.Env(srv.BaseURL(), selected.ID)

	// Resolve the ultra-zen binary path so the Workflow PreToolUse hook can
	// invoke `ultra-zen workflow-hook` by absolute path (works even if ultra-zen
	// is not on PATH). Fall back to "ultra-zen" if the path can't be resolved.
	hookBin := "ultra-zen"
	if exe, err := os.Executable(); err == nil {
		hookBin = exe
	}
	args := claude.Args(selected.ID, hookBin+" workflow-hook", claudeArgs)

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		cancel()
		die(fmt.Errorf("claude not found in PATH; install Claude Code first: %w", err))
	}

	cmd := exec.Command(claudePath, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cancel()
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		die(err)
	}
	cancel()
}

// waitForHealth polls the proxy health endpoint until it responds or the
// timeout elapses.
func waitForHealth(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("proxy health check timed out after %v: %w", timeout, lastErr)
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "ultra-zen: %v\n", err)
	os.Exit(1)
}

// redirectProxyLog points the standard logger (used by internal/proxy via
// log.Printf) at ~/.cache/ultra-zen/proxy.log so diagnostic output never
// leaks into Claude Code's TUI (which owns stderr). If the file can't be
// created, fall back to discarding logs — never stderr.
func redirectProxyLog() {
	dir := filepath.Join(os.Getenv("HOME"), ".cache", "ultra-zen")
	f, err := os.OpenFile(filepath.Join(dir, "proxy.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		_ = os.MkdirAll(dir, 0755)
		f, err = os.OpenFile(filepath.Join(dir, "proxy.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	}
	if err != nil {
		log.SetOutput(io.Discard)
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags)
}
