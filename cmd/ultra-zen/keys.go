// Keys subcommand: manage the persistent API-key store without opening the
// TUI. `ultra-zen keys` lists stored keys (names only — never the secrets),
// `ultra-zen keys set <provider> <key>` stores one, and
// `ultra-zen keys clear <provider>` removes one. Keys are also editable from
// the TUI model picker (press `k`).
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// stdout is an indirection over os.Stdout so tests can capture subcommand
// output.
var stdout io.Writer = os.Stdout

// knownKeyProviders is the set of providers ultra-zen can store a key for. It
// drives validation in the keys subcommand and doubles as a hint list.
var knownKeyProviders = []string{
	"openrouter",
	"modelscope",
	"groq",
	"cerebras",
	"huggingface",
	"cohere",
	"opencode-go",
}

// validKeyProvider reports whether name is a provider we know how to store a
// key for. The FreeTierProviders map is the source of truth for BYO-key
// providers; openrouter/opencode-go are handled explicitly.
func validKeyProvider(name string) bool {
	if name == "openrouter" || name == "opencode-go" {
		return true
	}
	if name == "codex" {
		return false // codex uses a URL, not a stored key
	}
	_, ok := models.FreeTierProviders[name]
	return ok
}

func cmdKeys(args []string) {
	switch {
	case len(args) == 0:
		listKeys()
	case args[0] == "set" && len(args) == 3:
		provider, value := args[1], args[2]
		if !validKeyProvider(provider) {
			fmt.Fprintf(os.Stderr, "ultra-zen: unknown provider %q; known: %s\n", provider, strings.Join(knownKeyProviders, ", "))
			os.Exit(1)
		}
		if strings.TrimSpace(value) == "" {
			fmt.Fprintln(os.Stderr, "ultra-zen: key cannot be empty; use `ultra-zen keys clear <provider>` to remove")
			os.Exit(1)
		}
		if err := keys.Save(provider, value); err != nil {
			die(fmt.Errorf("save key for %s: %w", provider, err))
		}
		fmt.Printf("stored key for %s (%s)\n", provider, keys.Path())
	case args[0] == "clear" && len(args) == 2:
		provider := args[1]
		if !validKeyProvider(provider) {
			fmt.Fprintf(os.Stderr, "ultra-zen: unknown provider %q; known: %s\n", provider, strings.Join(knownKeyProviders, ", "))
			os.Exit(1)
		}
		if err := keys.Save(provider, ""); err != nil {
			die(fmt.Errorf("clear key for %s: %w", provider, err))
		}
		fmt.Printf("cleared key for %s\n", provider)
	case args[0] == "path":
		fmt.Println(keys.Path())
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		keysUsage()
	default:
		fmt.Fprintln(os.Stderr, "ultra-zen: unrecognized keys command")
		keysUsage()
		os.Exit(1)
	}
}

func listKeys() {
	known := make([]string, 0, len(knownKeyProviders))
	seen := map[string]bool{}
	for _, p := range knownKeyProviders {
		if seen[p] {
			continue
		}
		seen[p] = true
		known = append(known, p)
	}
	for _, p := range known {
		status := "not set"
		if keys.Has(p) {
			status = "set"
		}
		fmt.Fprintf(stdout, "%-14s %s\n", p, status)
	}
	fmt.Fprintf(stdout, "\nKeys live in %s (mode 0600). Set with `ultra-zen keys set <provider> <key>`.\n", keys.Path())
}

func keysUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys                    list providers and whether a key is set")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys set <p> <key>      store an API key (e.g. modelscope, openrouter)")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys clear <p>          remove a stored key")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys path               print the keys directory")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Known providers: "+strings.Join(knownKeyProviders, ", "))
}
