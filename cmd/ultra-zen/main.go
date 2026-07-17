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
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/raketenkater/ultra-zen/internal/auth"
	"github.com/raketenkater/ultra-zen/internal/claude"
	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/proxy"
	"github.com/raketenkater/ultra-zen/internal/tui"
)

func main() {
	var (
		authPath  = flag.String("auth", "", "path to opencode auth.json (default: auto)")
		provider  = flag.String("provider", "opencode-go", "opencode auth provider name")
		port      = flag.Int("port", 8787, "local proxy listen port")
		listOnly  = flag.Bool("list", false, "list available models and exit")
		proxyOnly = flag.Bool("proxy-only", false, "start the proxy and block (for testing)")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "ultra-zen — run Claude Code on opencode Zen models")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  ultra-zen                 # pick a model, then launch claude")
		fmt.Fprintln(os.Stderr, "  ultra-zen <model>         # use this model id")
		fmt.Fprintln(os.Stderr, "  ultra-zen <model> -- <args>   # pass args through to claude")
		fmt.Fprintln(os.Stderr, "")
		flag.PrintDefaults()
	}
	flag.Parse()

	rest := flag.Args()
	var modelID string
	var claudeArgs []string
	if len(rest) > 0 {
		modelID = rest[0]
		if len(rest) > 1 {
			claudeArgs = rest[1:]
		}
	}

	store, err := auth.Load(*authPath)
	if err != nil {
		die(err)
	}
	key, err := auth.KeyFor(store, *provider)
	if err != nil {
		die(err)
	}

	httpClient := &http.Client{Timeout: 20 * time.Second}
	list, err := models.List(httpClient, key)
	if err != nil {
		die(err)
	}
	if len(list) == 0 {
		die(fmt.Errorf("no models available on the Zen gateway for the %s provider", *provider))
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
		modelID, err = tui.Run(list)
		if err != nil {
			die(fmt.Errorf("model selector: %w", err))
		}
		if modelID == "" {
			// User quit the selector.
			return
		}
	}
	selected := models.Find(list, modelID)
	if selected == nil {
		die(fmt.Errorf("model %q not found; run `ultra-zen --list` to see available models", modelID))
	}

	// Start the proxy.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := proxy.New(proxy.Config{
		BaseURL: selected.Base,
		APIKey:  key,
		Model:   selected.ID,
		Port:    *port,
	})
	if err := srv.Start(ctx); err != nil {
		die(fmt.Errorf("start proxy: %w", err))
	}
	if err := waitForHealth(srv.BaseURL(), 5*time.Second); err != nil {
		cancel()
		die(fmt.Errorf("proxy health check: %w", err))
	}

	if *proxyOnly {
		fmt.Fprintf(os.Stderr, "ultra-zen proxy ready on %s (model=%s, upstream=%s)\n", srv.BaseURL(), selected.ID, selected.Base)
		<-ctx.Done()
		return
	}

	fmt.Fprintf(os.Stderr, "\n  ultra-zen ▸ %s  (%s)\n  proxy on %s  →  claude\n\n",
		selected.ID, selected.Base, srv.BaseURL())

	// Forward SIGINT/SIGTERM to claude and tear down the proxy.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	env := claude.Env(srv.BaseURL(), selected.ID)
	args := claude.Args(claudeArgs)

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
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "ultra-zen: %v\n", err)
	os.Exit(1)
}