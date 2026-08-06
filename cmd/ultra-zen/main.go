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
	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/proxy"
	"github.com/raketenkater/ultra-zen/internal/tui"
	"github.com/raketenkater/ultra-zen/internal/workflow"
)

// Version is set at build time via -ldflags. Falls back to "dev" in local builds.
var Version = "dev"

// autoSourceMaxRoutes caps how many routes a single provider contributes to the
// automatic free pool. A daily free allocation is account-wide (the proxy
// retires a provider's routes together on exhaustion), so sibling models only
// help against per-model failures; an uncapped catalog would bury the
// cross-provider routes that actually survive an exhausted account.
const autoSourceMaxRoutes = 3

// autoSourceOrder is the display order for probing other BYO free-tier
// providers during automatic pool discovery (after the active provider, Zen,
// and OpenRouter). Only providers with an already-stored key are ever loaded.
var autoSourceOrder = []string{"modelscope", "groq", "cerebras", "huggingface", "cohere"}

// modelFlag is a repeatable, comma-friendly model list. Keeping each selected
// model explicit makes a free pool deterministic while still allowing the
// convenient `--free-model a,b,c` spelling.
type modelFlag []string

func (f *modelFlag) String() string { return strings.Join(*f, ",") }

func (f *modelFlag) Set(value string) error {
	for _, id := range strings.Split(value, ",") {
		if id = strings.TrimSpace(id); id != "" {
			*f = append(*f, id)
		}
	}
	return nil
}

// applySavedFreePool makes the TUI's last saved rotation the default for a
// direct model launch too. Explicit --free-model and --worker flags remain
// authoritative; an interactive launch gets the same state from tui.Run.
func applySavedFreePool(freeModels modelFlag, modelID string, cliWorkerRequested bool) (modelFlag, bool) {
	if len(freeModels) > 0 || modelID == "" || cliWorkerRequested {
		return freeModels, len(freeModels) > 0
	}
	for _, route := range tui.LoadFreePool() {
		_ = freeModels.Set(route.String())
	}
	return freeModels, len(freeModels) > 0
}

// tuiLaunchArgs records the finalized interactive choice rather than the raw
// argv that opened the picker. Without this, a TUI launch records no model and
// resume either opens the picker again or replays the model on the wrong
// default provider.
func tuiLaunchArgs(model, provider, worker string, freeModels modelFlag, port, openRouterRPM int) []string {
	args := []string{model, "--provider", provider}
	if worker != "" {
		args = append(args, "--worker", worker)
	}
	for _, route := range freeModels {
		args = append(args, "--free-model", route)
	}
	if port != 0 {
		args = append(args, "--port", strconv.Itoa(port))
	}
	if openRouterRPM != 20 {
		args = append(args, "--openrouter-rpm", strconv.Itoa(openRouterRPM))
	}
	return args
}

// splitFreeModelSpec accepts provider-qualified free routes while keeping bare
// OpenRouter model IDs backward compatible. The BYO-key providers
// (modelscope/groq/cerebras/huggingface/cohere) mirror models.FreeTierProviders;
// codex is recognized but its models are subscription-backed (Free:false) and
// are rejected by addRoute. Examples:
//
//	opencode:deepseek-v4-flash-free
//	openrouter:qwen/qwen3-coder:free
//	modelscope:deepseek-ai/DeepSeek-V4-Flash
//	openrouter/free
func splitFreeModelSpec(value string) (provider, model string, err error) {
	value = strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(value, "opencode:"):
		provider, model = "opencode-go", strings.TrimPrefix(value, "opencode:")
	case strings.HasPrefix(value, "opencode-go:"):
		provider, model = "opencode-go", strings.TrimPrefix(value, "opencode-go:")
	case strings.HasPrefix(value, "openrouter:"):
		provider, model = "openrouter", strings.TrimPrefix(value, "openrouter:")
	case strings.HasPrefix(value, "modelscope:"):
		provider, model = "modelscope", strings.TrimPrefix(value, "modelscope:")
	case strings.HasPrefix(value, "groq:"):
		provider, model = "groq", strings.TrimPrefix(value, "groq:")
	case strings.HasPrefix(value, "cerebras:"):
		provider, model = "cerebras", strings.TrimPrefix(value, "cerebras:")
	case strings.HasPrefix(value, "huggingface:"):
		provider, model = "huggingface", strings.TrimPrefix(value, "huggingface:")
	case strings.HasPrefix(value, "cohere:"):
		provider, model = "cohere", strings.TrimPrefix(value, "cohere:")
	case strings.HasPrefix(value, "codex:"):
		provider, model = "codex", strings.TrimPrefix(value, "codex:")
	default:
		provider, model = "openrouter", value
	}
	if strings.TrimSpace(model) == "" {
		return "", "", fmt.Errorf("free model specification %q has no model id", value)
	}
	return provider, model, nil
}

func freeTierModels(list []models.Model) []models.Model {
	free := make([]models.Model, 0, len(list))
	for _, model := range list {
		if model.Free {
			free = append(free, model)
		}
	}
	return models.SortByRecent(free, models.LoadRecent())
}

// loadTUIProvider switches the primary backend when the all-provider start
// screen selects a model outside the provider main initially loaded. The TUI
// only offers providers whose credentials were discovered, so this path never
// opens another prompt; it resolves the same flag/env/store/auth precedence as
// the normal startup path and verifies the model list again before launch.
func loadTUIProvider(client *http.Client, provider, authPath, openRouterFlag, apiFlag string) ([]models.Model, string, error) {
	switch {
	case provider == "openrouter":
		key := models.ProviderKey(provider, openRouterFlag, "")
		if key == "" {
			return nil, "", fmt.Errorf("OpenRouter key is no longer available")
		}
		list, err := models.ListOpenRouter(client, key)
		return list, key, err
	case provider == "opencode-go":
		key := keys.Load("opencode-go")
		if key == "" {
			store, err := auth.Load(authPath)
			if err != nil {
				return nil, "", err
			}
			key, err = auth.KeyFor(store, "opencode-go")
			if err != nil {
				return nil, "", err
			}
		}
		list, err := models.List(client, key)
		return list, key, err
	default:
		_, ok := models.FreeTierProviders[provider]
		if !ok {
			return nil, "", fmt.Errorf("unknown TUI provider %q", provider)
		}
		key := models.ProviderKey(provider, apiFlag, "")
		if key == "" {
			return nil, "", fmt.Errorf("%s key is no longer available", provider)
		}
		list, err := models.ListFreeTierProvider(client, provider, key)
		return list, key, err
	}
}

func main() {
	// Subcommand dispatch (before flag parsing, which would choke on the
	// subcommand). `workflow-hook` is invoked by Claude Code's PreToolUse hook
	// to deterministically rewrite Workflow agent() scripts with a safe stallMs.
	if len(os.Args) > 1 && os.Args[1] == "workflow-hook" {
		workflow.RunHook()
		return
	}
	// `sessions` lists recorded resumable Claude Code sessions for this
	// directory; `resume` reopens one, replaying the launch it was recorded
	// under. See session.go.
	if len(os.Args) > 1 && (os.Args[1] == "sessions" || os.Args[1] == "resume") {
		cmdSessions(os.Args[1], os.Args[2:])
		return
	}
	// `keys` manages the persistent API-key store without the TUI. See keys.go.
	if len(os.Args) > 1 && os.Args[1] == "keys" {
		cmdKeys(os.Args[2:])
		return
	}

	// Redirect the proxy's log output (log.Printf in internal/proxy) to a file
	// instead of stderr. Claude Code's TUI owns stderr, so any log line written
	// there leaks into the front-end as garbled text. Write to
	// ~/.cache/ultra-zen/proxy.log so diagnostics are still available.
	redirectProxyLog()

	// The raw invocation, before flag parsing rewrites it, is what a later
	// `ultra-zen resume` replays to reproduce this exact launch.
	originalArgs := stripResumeArgs(os.Args[1:])

	var freeModels modelFlag
	var (
		authPath      = flag.String("auth", "", "path to opencode auth.json (default: auto)")
		provider      = flag.String("provider", "opencode-go", "backend provider: opencode-go, openrouter, or codex")
		openRouterKey = flag.String("openrouter-key", "", "OpenRouter API key (or set OPENROUTER_API_KEY)")
		codexBaseURL  = flag.String("codex-url", "", "Codex endpoint base URL (or set CODEX_BASE_URL), e.g. http://127.0.0.1:8000/v1")
		codexKey      = flag.String("codex-key", "", "Codex endpoint API key (or set CODEX_API_KEY)")
		apiKey        = flag.String("api-key", "", "API key for --provider groq/cerebras/huggingface/cohere (or set that provider's own env var)")
		workerModel   = flag.String("worker", "", "cheaper model for background sub-agents (orchestrator/worker split)")
		openRouterRPM = flag.Int("openrouter-rpm", 20, "pace OpenRouter free requests per minute (0 disables pacing)")
		port          = flag.Int("port", 0, "local proxy listen port (0 = pick a free port per instance)")
		listOnly      = flag.Bool("list", false, "list available models and exit")
		proxyOnly     = flag.Bool("proxy-only", false, "start the proxy and block (for testing)")
		showVer       = flag.Bool("version", false, "print version and exit")
		resumeSession = flag.String("resume-session", "", "reopen a recorded ultra-zen session (session-id or \"latest\"); see `ultra-zen resume`")
	)
	flag.Var(&freeModels, "free-model", "free fallback as [openrouter:|opencode:]model; repeat or comma-separate (replaces --worker)")
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
		fmt.Fprintln(os.Stderr, "  --provider groq          Groq free tier (set GROQ_API_KEY or --api-key)")
		fmt.Fprintln(os.Stderr, "  --provider cerebras      Cerebras free tier (set CEREBRAS_API_KEY or --api-key)")
		fmt.Fprintln(os.Stderr, "  --provider huggingface   HuggingFace Inference router (set HF_TOKEN or --api-key)")
		fmt.Fprintln(os.Stderr, "  --provider cohere        Cohere free trial tier (set COHERE_API_KEY or --api-key)")
		fmt.Fprintln(os.Stderr, "  --provider modelscope    ModelScope API-Inference free tier (set MODELSCOPE_API_KEY or --api-key)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Resilient free-model pool (recommended for free sessions):")
		fmt.Fprintln(os.Stderr, "  --free-model <route>     Add [openrouter:|opencode:]model fallback (repeatable)")
		fmt.Fprintln(os.Stderr, "  --openrouter-rpm <n>     Pace shared OpenRouter traffic (default 20)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Legacy orchestrator/worker split:")
		fmt.Fprintln(os.Stderr, "  --worker <model>         Use a cheaper model for background sub-agents")
		fmt.Fprintln(os.Stderr, "")
		flag.PrintDefaults()
	}
	flag.Parse()
	cliWorkerRequested := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "worker" {
			cliWorkerRequested = true
		}
	})

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
			cliWorkerRequested = true
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--worker="):
			*workerModel = strings.TrimPrefix(arg, "--worker=")
			cliWorkerRequested = true
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--free-model" && i+1 < len(claudeArgs):
			_ = freeModels.Set(claudeArgs[i+1])
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--free-model="):
			_ = freeModels.Set(strings.TrimPrefix(arg, "--free-model="))
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--openrouter-rpm" && i+1 < len(claudeArgs):
			value, err := strconv.Atoi(claudeArgs[i+1])
			if err != nil {
				die(fmt.Errorf("invalid --openrouter-rpm %q", claudeArgs[i+1]))
			}
			*openRouterRPM = value
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--openrouter-rpm="):
			raw := strings.TrimPrefix(arg, "--openrouter-rpm=")
			value, err := strconv.Atoi(raw)
			if err != nil {
				die(fmt.Errorf("invalid --openrouter-rpm %q", raw))
			}
			*openRouterRPM = value
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
		case arg == "--resume-session" && i+1 < len(claudeArgs):
			*resumeSession = claudeArgs[i+1]
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--resume-session="):
			*resumeSession = strings.TrimPrefix(arg, "--resume-session=")
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		case arg == "--api-key" && i+1 < len(claudeArgs):
			*apiKey = claudeArgs[i+1]
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+2:]...)
		case strings.HasPrefix(arg, "--api-key="):
			*apiKey = strings.TrimPrefix(arg, "--api-key=")
			claudeArgs = append(claudeArgs[:i], claudeArgs[i+1:]...)
		default:
			i++
		}
	}

	freeModels, freePoolRequested := applySavedFreePool(freeModels, modelID, cliWorkerRequested)
	// A free pool can be the complete launch specification: the first model is
	// the primary and the remainder are ordered fallbacks on OpenRouter.
	if modelID == "" && freePoolRequested {
		poolProvider, poolModel, err := splitFreeModelSpec(freeModels[0])
		if err != nil {
			die(err)
		}
		*provider = poolProvider
		modelID = poolModel
		freeModels = freeModels[1:]
	}
	if *openRouterRPM < 0 {
		die(fmt.Errorf("--openrouter-rpm must be zero or greater"))
	}

	httpClient := &http.Client{Timeout: 20 * time.Second}
	var list []models.Model
	var key string

	// interactive is true when no model was named on the CLI, so it's safe to
	// open a TUI prompt for a missing key rather than exiting.
	interactive := modelID == "" && !*listOnly

	switch def, isFreeTier := models.FreeTierProviders[*provider]; {
	case isFreeTier:
		k := *apiKey
		if k == "" {
			k = os.Getenv(def.EnvKey)
		}
		if k == "" {
			k = keys.Load(*provider)
		}
		if k == "" && interactive {
			// Open the complete launcher with an empty primary catalog. Its key
			// manager can configure this or any other provider without requiring
			// the user to know a CLI flag first.
			break
		}
		if k == "" {
			die(fmt.Errorf("%s requires an API key: set %s or pass --api-key\nGet one at %s", *provider, def.EnvKey, def.KeyHint))
		}
		key = k
		var err error
		list, err = models.ListFreeTierProvider(httpClient, *provider, key)
		if err != nil {
			die(fmt.Errorf("%s: %w", *provider, err))
		}
	case *provider == "openrouter":
		k := *openRouterKey
		if k == "" {
			k = os.Getenv("OPENROUTER_API_KEY")
		}
		if k == "" {
			k = keys.Load("openrouter")
		}
		if k == "" && interactive {
			break
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
	case *provider == "codex":
		base := *codexBaseURL
		if base == "" {
			base = os.Getenv("CODEX_BASE_URL")
		}
		if base == "" && interactive {
			base = tui.PromptKey("Codex endpoint base URL", "e.g. http://127.0.0.1:8000/v1 (ChatMock)", false)
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
	case *provider == "opencode-go":
		storedKey := keys.Load("opencode-go")
		if storedKey != "" {
			key = storedKey
			var err error
			list, err = models.List(httpClient, key)
			if err != nil {
				die(err)
			}
			break
		}
		store, err := auth.Load(*authPath)
		if err != nil {
			// With no opencode login, still open the complete TUI. Background
			// discovery will show every provider that already has a key, and k
			// can configure one when none are present.
			if interactive {
				break
			}
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
	default:
		die(fmt.Errorf("unknown provider %q", *provider))
	}
	if len(list) == 0 && !interactive {
		die(fmt.Errorf("no models available for provider %q", *provider))
	}

	if *listOnly {
		for i, m := range list {
			tag := ""
			if m.Free {
				tag = " (free)"
			}
			ctx := ""
			if m.ContextLength > 0 {
				ctx = fmt.Sprintf("  [%dk]", m.ContextLength/1024)
			}
			fmt.Printf("%2d. %-28s  %s%s%s\n", i+1, m.ID, m.Base, tag, ctx)
		}
		return
	}

	var tuiFreePool []tui.FreeRoute
	launchedFromTUI := false
	if modelID == "" {
		var workerPick, resumeID, tuiProvider string
		var quit bool
		res := tui.Run(list, *provider, buildResumeOption())
		modelID, tuiProvider, workerPick, resumeID, quit = res.Choice, res.Provider, res.Worker, res.ResumeSessionID, res.Quit
		tuiFreePool = res.FreePool
		if resumeID != "" {
			cmdSessionResume(resumeID, nil)
			return
		}
		if quit || modelID == "" {
			return
		}
		launchedFromTUI = true
		if tuiProvider != "" && (tuiProvider != *provider || len(list) == 0 || key == "") {
			var err error
			list, key, err = loadTUIProvider(httpClient, tuiProvider, *authPath, *openRouterKey, *apiKey)
			if err != nil {
				die(fmt.Errorf("load TUI provider %s: %w", tuiProvider, err))
			}
			*provider = tuiProvider
		}
		// CLI --worker flag overrides TUI pick; TUI pick fills the default.
		if *workerModel == "" && workerPick != "" && !freePoolRequested {
			*workerModel = workerPick
		}
	}
	// A TUI-configured pool (from the 'f' screen) is folded into freeModels when
	// no --free-model flags were given (explicit CLI flags win, most explicit).
	if !freePoolRequested && !cliWorkerRequested && len(tuiFreePool) > 0 {
		for _, route := range tuiFreePool {
			_ = freeModels.Set(route.String())
		}
		freePoolRequested = true
		// The fallback pool is the TUI replacement for the legacy combo worker.
		// A combo selected after configuring the pool must not re-enable both.
		*workerModel = ""
	}
	selected := models.Find(list, modelID)
	if selected == nil && len(freeModels) > 0 {
		// The configured primary is no longer served by its provider (e.g. a
		// stale saved pool route). Promote the first still-available route
		// from the pool instead of aborting the launch; the dead route is
		// pruned from the saved pool below.
		for _, raw := range freeModels {
			poolProvider, poolModel, err := splitFreeModelSpec(raw)
			if err != nil {
				die(err)
			}
			plist, pkey := list, key
			if poolProvider != *provider {
				plist, pkey, err = loadTUIProvider(httpClient, poolProvider, *authPath, *openRouterKey, *apiKey)
				if err != nil {
					continue
				}
			}
			candidate := models.Find(plist, poolModel)
			if candidate == nil || !candidate.Free {
				continue
			}
			warn("primary model %q is no longer available; promoting pool route %s", modelID, raw)
			list, key = plist, pkey
			*provider = poolProvider
			selected = candidate
			modelID = candidate.ID
			break
		}
	}
	if selected == nil {
		die(fmt.Errorf("model %q not found; run `ultra-zen --list` to see available models", modelID))
	}
	models.RecordRecent(selected.ID)
	models.RecordCombo(selected.ID, *workerModel)

	// Build a provider-aware free pool. Permanent daily/free-allocation limits
	// switch providers (OpenRouter <-> opencode Zen), while temporary throttles
	// may still try another model within the active provider.
	explicitFreePool := freePoolRequested
	if explicitFreePool && *workerModel != "" {
		die(fmt.Errorf("--free-model replaces the orchestrator/worker split and cannot be combined with --worker"))
	}

	var fallbackRoutes []proxy.Upstream
	var openRouterList []models.Model
	openRouterPoolKey := *openRouterKey
	if openRouterPoolKey == "" {
		openRouterPoolKey = os.Getenv("OPENROUTER_API_KEY")
	}
	if openRouterPoolKey == "" {
		openRouterPoolKey = keys.Load("openrouter")
	}
	openRouterLoaded := false
	if *provider == "openrouter" {
		openRouterPoolKey, openRouterList, openRouterLoaded = key, list, true
	}
	ensureOpenRouter := func(prompt bool) error {
		if openRouterLoaded {
			return nil
		}
		if openRouterPoolKey == "" && prompt && interactive {
			openRouterPoolKey = tui.PromptKey("OpenRouter API key for the free pool", "get one at https://openrouter.ai/keys", true)
			if openRouterPoolKey != "" {
				_ = keys.Save("openrouter", openRouterPoolKey) // remember for next time
			}
		}
		if openRouterPoolKey == "" {
			return fmt.Errorf("set OPENROUTER_API_KEY or pass --openrouter-key")
		}
		var err error
		openRouterList, err = models.ListOpenRouter(httpClient, openRouterPoolKey)
		if err != nil {
			return err
		}
		openRouterLoaded = true
		return nil
	}

	// ensureFreeTier lazily loads the free-model list for a BYO-key free-tier
	// provider (modelscope/groq/cerebras/huggingface/cohere). Caches per
	// provider. Requires the key to already exist (flag/env/keys store) — no
	// interactive prompt, because a TUI-configured pool already implies the user
	// saw a key prompt in the picker.
	freeTierLists := map[string][]models.Model{}
	freeTierKeys := map[string]string{}
	ensureFreeTier := func(p string) error {
		if _, ok := freeTierLists[p]; ok {
			return nil
		}
		def, ok := models.FreeTierProviders[p]
		if !ok {
			return fmt.Errorf("unknown free-tier provider %q", p)
		}
		k := ""
		if p == *provider {
			k = key
		} else {
			k = models.ProviderKey(p, "", "")
		}
		if k == "" {
			return fmt.Errorf("no key for %s; set %s or --api-key", p, def.EnvKey)
		}
		freeTierKeys[p] = k
		list, err := models.ListFreeTierProvider(httpClient, p, k)
		if err != nil {
			return err
		}
		freeTierLists[p] = list
		return nil
	}

	var zenList []models.Model
	var zenPoolKey string
	zenLoaded := false
	if *provider == "opencode-go" {
		zenPoolKey, zenList, zenLoaded = key, list, true
	}
	ensureZen := func() error {
		if zenLoaded {
			return nil
		}
		zenPoolKey = keys.Load("opencode-go")
		if zenPoolKey != "" {
			var err error
			zenList, err = models.ListZenFree(httpClient, zenPoolKey)
			if err != nil {
				return err
			}
			zenLoaded = true
			return nil
		}
		store, err := auth.Load(*authPath)
		if err != nil {
			return err
		}
		zenPoolKey, err = auth.KeyFor(store, "opencode-go")
		if err != nil {
			return err
		}
		zenList, err = models.ListZenFree(httpClient, zenPoolKey)
		if err != nil {
			return err
		}
		zenLoaded = true
		return nil
	}

	seenRoutes := map[string]bool{selected.Base + "\x00" + selected.ID: true}
	addRoute := func(provider string, model *models.Model, routeKey string) {
		if model == nil || !model.Free {
			return
		}
		key := model.Base + "\x00" + model.ID
		if seenRoutes[key] {
			return
		}
		seenRoutes[key] = true
		fallbackRoutes = append(fallbackRoutes, proxy.Upstream{
			Provider: provider,
			BaseURL:  model.Base,
			APIKey:   routeKey,
			Model:    model.ID,
		})
	}

	// staleRoutes remembers pool entries that are permanently gone from their
	// provider's live catalog. They are skipped with a warning instead of
	// aborting the launch, and pruned from the saved pool afterwards so the
	// next launch starts clean.
	var staleRoutes []string

	// With no explicit remainder (including a one-model --free-model launch),
	// discover free routes from every provider whose credentials already exist
	// on the machine. No extra credential prompt is introduced for an automatic
	// alternate. Free models are rotation: they back a paid primary as
	// fallbacks, and a free primary gets the other providers' free routes.
	//
	// Each source contributes at most autoSourceMaxRoutes so a large catalog
	// (e.g. OpenRouter's hundreds of :free models) cannot bury the cross-provider
	// routes that actually survive an exhausted account. The active provider's
	// own free catalog is probed first, then opencode Zen, then OpenRouter, then
	// any other BYO free tier whose key is already stored. Every skipped source
	// is reported so a silently empty pool is never launched without feedback.
	automaticPool := !explicitFreePool || len(freeModels) == 0
	if automaticPool && *workerModel == "" {
		var skipped []string
		route := func(p string, model *models.Model, poolKey string) {
			if model == nil || !model.Free {
				return
			}
			addRoute(p, model, poolKey)
		}

		// 1. The active provider's own free catalog (BYO free tiers mark every
		// model Free). The primary is excluded from its own provider's siblings.
		own := freeTierModels(list)
		for i := range own {
			if i >= autoSourceMaxRoutes {
				break
			}
			route(*provider, &own[i], key)
		}
		// When the active provider IS the source, its own siblings already filled
		// up to autoSourceMaxRoutes — that alone is a pool, so stop early.

		// 2. opencode Zen free tier (if not the active provider).
		if *provider != "opencode-go" {
			if err := ensureZen(); err == nil {
				free := freeTierModels(zenList)
				for i := range free {
					if i >= autoSourceMaxRoutes {
						break
					}
					route("opencode-go", &free[i], zenPoolKey)
				}
			} else {
				skipped = append(skipped, "opencode Zen: "+err.Error())
			}
		}
		// 3. OpenRouter free router (if not the active provider).
		if *provider != "openrouter" {
			if err := ensureOpenRouter(false); err == nil && selected.ID != "openrouter/free" {
				route("openrouter", models.Find(openRouterList, "openrouter/free"), openRouterPoolKey)
			} else if err != nil {
				skipped = append(skipped, "OpenRouter: "+err.Error())
			}
		}
		// 4. Other BYO free tiers whose keys are already stored. Never prompts
		// for a new credential — ProviderKey returns "" when no key exists.
		for _, p := range autoSourceOrder {
			if p == *provider || p == "opencode-go" || p == "openrouter" {
				continue
			}
			if err := ensureFreeTier(p); err != nil {
				skipped = append(skipped, p+": "+err.Error())
				continue
			}
			free := freeTierModels(freeTierLists[p])
			for i := range free {
				if i >= autoSourceMaxRoutes {
					break
				}
				route(p, &free[i], freeTierKeys[p])
			}
		}

		// Report every skipped source so an empty pool is never silent.
		if len(fallbackRoutes) == 0 && len(skipped) > 0 {
			for _, s := range skipped {
				warn("automatic free rotation unavailable — %s", s)
			}
			warn("no automatic free rotation configured; set an OpenRouter/Zen key or run the TUI 'f' (Free cycle) screen")
		}
	} else if explicitFreePool {
		for _, raw := range freeModels {
			poolProvider, id, err := splitFreeModelSpec(raw)
			if err != nil {
				die(err)
			}
			switch poolProvider {
			case "openrouter":
				if err := ensureOpenRouter(true); err != nil {
					warn("skip OpenRouter free fallback %q: %v", id, err)
					continue
				}
				fallback := models.Find(openRouterList, id)
				if fallback == nil || !fallback.Free {
					warn("OpenRouter free fallback %q is no longer available; skipping", id)
					staleRoutes = append(staleRoutes, raw)
					continue
				}
				addRoute("openrouter", fallback, openRouterPoolKey)
			case "opencode-go":
				if err := ensureZen(); err != nil {
					warn("skip opencode Zen free fallback %q: %v", id, err)
					continue
				}
				fallback := models.Find(zenList, id)
				if fallback == nil || !fallback.Free {
					warn("opencode Zen free fallback %q is no longer available; skipping", id)
					staleRoutes = append(staleRoutes, raw)
					continue
				}
				addRoute("opencode-go", fallback, zenPoolKey)
			case "modelscope", "groq", "cerebras", "huggingface", "cohere":
				if err := ensureFreeTier(poolProvider); err != nil {
					warn("skip %s free fallback %q: %v", poolProvider, id, err)
					continue
				}
				fallback := models.Find(freeTierLists[poolProvider], id)
				if fallback == nil || !fallback.Free {
					warn("%s free fallback %q is no longer available; skipping", poolProvider, id)
					staleRoutes = append(staleRoutes, raw)
					continue
				}
				addRoute(poolProvider, fallback, freeTierKeys[poolProvider])
			case "codex":
				die(fmt.Errorf("codex model %q cannot be used as a free fallback; codex endpoints are subscription-backed", id))
			default:
				die(fmt.Errorf("unknown free fallback provider %q", poolProvider))
			}
		}
	}

	// A free cycle must never collapse to a single provider (or zero routes):
	// one daily-limit exhaust on that provider would leave routeOrder empty and
	// the proxy would answer every request with the 429 "every configured free
	// model is exhausted". When an explicit pool assembles to fewer than two
	// distinct providers, fall back to automatic cross-provider discovery so the
	// session always has an independent escape hatch.
	if len(fallbackRoutes) > 0 {
		providers := map[string]bool{}
		for _, route := range fallbackRoutes {
			providers[route.Provider] = true
		}
		if len(providers) < 2 && *workerModel == "" {
			warn("free cycle has only %d provider(s) after filtering; adding auto-discovered free routes for resilience", len(providers))
			// Mirror the automaticPool branch above: add openrouter/free (when
			// the primary isn't already OpenRouter) plus the zen *-free list, so
			// the pool spans at least two providers. Each addRoute dedups on
			// base+model, so existing routes are not duplicated.
			if selected.Base != models.OpenRouterBase {
				if err := ensureOpenRouter(false); err == nil && selected.ID != "openrouter/free" {
					addRoute("openrouter", models.Find(openRouterList, "openrouter/free"), openRouterPoolKey)
				}
			}
			if err := ensureZen(); err == nil {
				free := freeTierModels(zenList)
				for i := range free {
					addRoute("opencode-go", &free[i], zenPoolKey)
				}
			}
		}
	}

	// Refresh the saved pool on every launch: drop routes that no longer exist
	// in a provider's live catalog so a dead entry can never block a launch.
	if len(staleRoutes) > 0 {
		dead := make(map[string]bool, len(staleRoutes))
		for _, r := range staleRoutes {
			dead[r] = true
		}
		var keep []tui.FreeRoute
		for _, route := range tui.LoadFreePool() {
			if !dead[route.String()] {
				keep = append(keep, route)
			}
		}
		if err := tui.SaveFreePool(keep); err != nil {
			warn("could not prune stale routes from saved free pool: %v", err)
		}
	}

	// Build the model list for /v1/models (Claude Code's /model command).
	modelInfos := make([]proxy.ModelInfo, 0, len(list))
	for _, m := range list {
		modelInfos = append(modelInfos, proxy.ModelInfo{ID: m.ID, Name: m.Name})
	}
	for _, route := range fallbackRoutes {
		if models.Find(list, route.Model) == nil {
			modelInfos = append(modelInfos, proxy.ModelInfo{ID: route.Model, Name: route.Model})
		}
	}

	// Start the proxy.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := proxy.New(proxy.Config{
		Provider:      *provider,
		BaseURL:       selected.Base,
		APIKey:        key,
		Model:         selected.ID,
		WorkerModel:   *workerModel,
		Fallbacks:     fallbackRoutes,
		OpenRouterRPM: *openRouterRPM,
		Port:          *port,
		Models:        modelInfos,
		OnUnavailable: func(route proxy.Upstream) {
			if err := models.MarkUnavailable(route.Provider, route.Model); err != nil {
				log.Printf("ultra-zen: could not remember unavailable %s model %s: %v", route.Provider, route.Model, err)
			}
		},
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
	for i, fallback := range fallbackRoutes {
		fmt.Fprintf(os.Stderr, "  fallback %d ▸ %s  (%s)\n", i+1, fallback.Model, fallback.BaseURL)
	}
	fmt.Fprintf(os.Stderr, "  proxy on %s  →  claude\n\n", srv.BaseURL())

	// Forward SIGINT/SIGTERM to claude and tear down the proxy.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	env := claude.Env(srv.BaseURL(), selected.ID, selected.ContextLength)

	// Resolve the ultra-zen binary path so the Workflow PreToolUse hook can
	// invoke `ultra-zen workflow-hook` by absolute path (works even if ultra-zen
	// is not on PATH). Fall back to "ultra-zen" if the path can't be resolved.
	hookBin := "ultra-zen"
	if exe, err := os.Executable(); err == nil {
		hookBin = exe
	}
	args := claude.Args(selected.ID, hookBin+" workflow-hook", claudeArgs)

	cacheDir := sessionCacheDir()
	sessionLaunchArgs := originalArgs
	if launchedFromTUI {
		sessionLaunchArgs = tuiLaunchArgs(selected.ID, *provider, *workerModel, freeModels, *port, *openRouterRPM)
	}
	sessionSpec, sessionErr := resolveLaunchSession(cacheDir, *resumeSession, *provider, selected.ID, *workerModel, *port, sessionLaunchArgs)
	if sessionErr != nil {
		cancel()
		die(fmt.Errorf("resume: %w", sessionErr))
	}
	sessionArgs, err := sessionClaudeArgs(sessionSpec, claudeArgs, args)
	if err != nil {
		cancel()
		die(err)
	}
	args = sessionArgs
	if summary := describeSessionResume(sessionSpec); summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}
	// The resume instruction goes last so it becomes Claude's opening turn.
	if prompt := sessionResumePrompt(sessionSpec); prompt != "" && sessionSpec.Resume {
		args = append(args, prompt)
	}

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
	runErr := cmd.Run()
	// Record on exit as well as on launch: the workflow run ID is assigned
	// inside Claude Code, so only now is the resume handle complete.
	refreshSessionRecord(cacheDir, sessionSpec, *provider, selected.ID, *workerModel, *port, sessionLaunchArgs)
	if runErr != nil {
		cancel()
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		die(runErr)
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

// warn reports a non-fatal problem to stderr (e.g. a stale free-pool route
// that is skipped instead of aborting the launch).
func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ultra-zen: "+format+"\n", args...)
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
