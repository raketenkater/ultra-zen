// Keys subcommand: manage the persistent API-key store without opening the
// TUI. `ultra-zen keys` lists stored keys (names only — never the secrets),
// `ultra-zen keys set <provider> <key>` stores one (`-` reads from stdin), and
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
var stdin io.Reader = os.Stdin

// knownKeyProviders is the set of providers ultra-zen can store a key for. It
// drives validation in the keys subcommand and doubles as a hint list.
var knownKeyProviders = []string{
	"openrouter",
	"modelscope",
	"groq",
	"cerebras",
	"huggingface",
	"cohere",
	"saia",
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

// cmdKeys is the `ultra-zen keys` entry point. An optional leading --system
// (or -s) targets the machine-wide store at /etc/ultra-zen/keys instead of the
// per-user store. Writing the system store requires root; a non-root caller
// gets a sudo hint instead of a silent fallback to the user store.
func cmdKeys(args []string) {
	system := false
	rest := args
	if len(args) > 0 && (args[0] == "--system" || args[0] == "-s") {
		system = true
		rest = args[1:]
	}
	store := keys.StoreUser
	if system {
		store = keys.StoreSystem
	}
	switch {
	case len(rest) == 0:
		if system {
			listSystemKeys()
			return
		}
		listKeys()
	case rest[0] == "set" && len(rest) == 3:
		provider, value := rest[1], rest[2]
		if !validKeyProvider(provider) {
			fmt.Fprintf(os.Stderr, "ultra-zen: unknown provider %q; known: %s\n", provider, strings.Join(knownKeyProviders, ", "))
			os.Exit(1)
		}
		if value == "-" {
			data, err := io.ReadAll(stdin)
			if err != nil {
				die(fmt.Errorf("read key from stdin: %w", err))
			}
			value = string(data)
		}
		if strings.TrimSpace(value) == "" {
			fmt.Fprintln(os.Stderr, "ultra-zen: key cannot be empty; use `ultra-zen keys clear <provider>` to remove")
			os.Exit(1)
		}
		if err := saveKeyForStore(store, provider, value); err != nil {
			die(fmt.Errorf("save key for %s: %w", provider, err))
		}
		_ = models.ClearUnavailable(provider)
		fmt.Printf("stored %s key for %s (%s)\n", storeName(system), provider, keys.PathFor(store))
	case rest[0] == "clear" && len(rest) == 2:
		provider := rest[1]
		if !validKeyProvider(provider) {
			fmt.Fprintf(os.Stderr, "ultra-zen: unknown provider %q; known: %s\n", provider, strings.Join(knownKeyProviders, ", "))
			os.Exit(1)
		}
		if err := saveKeyForStore(store, provider, ""); err != nil {
			die(fmt.Errorf("clear key for %s: %w", provider, err))
		}
		_ = models.ClearUnavailable(provider)
		fmt.Printf("cleared %s key for %s\n", storeName(system), provider)
	case rest[0] == "path":
		fmt.Println(keys.PathFor(store))
	case rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help":
		keysUsage()
	default:
		fmt.Fprintln(os.Stderr, "ultra-zen: unrecognized keys command")
		keysUsage()
		os.Exit(1)
	}
}

// storeName labels a store in user-facing output.
func storeName(system bool) string {
	if system {
		return "system"
	}
	return "user"
}

// saveKeyForStore writes a key to the requested store. Writing the system
// store when not root fails with a clear sudo hint — never a silent fallback
// to the user store.
func saveKeyForStore(store keys.Store, provider, value string) error {
	if store == keys.StoreSystem {
		if err := keys.SaveSystem(provider, value); err != nil {
			return fmt.Errorf("system key store %s not writable (run with sudo): %w", keys.SystemDir(), err)
		}
		return nil
	}
	return keys.Save(provider, value)
}

// listSystemKeys prints the system store's status per provider.
func listSystemKeys() {
	for _, p := range knownKeyProviders {
		status := "not set"
		if keys.HasIn(p, keys.StoreSystem) {
			status = "set"
		}
		fmt.Fprintf(stdout, "%-14s %s\n", p, status)
	}
	fmt.Fprintf(stdout, "\nSystem keys live in %s (root-writable, world-readable 0644).\n", keys.SystemDir())
	fmt.Fprintf(stdout, "Set with: sudo ultra-zen keys --system set <provider> <key>\n")
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
	fmt.Fprintf(stdout, "\nKeys live in %s (mode 0600). Set with `ultra-zen keys set <provider> <key>` or pipe to `ultra-zen keys set <provider> -`.\n", keys.Path())
}

func keysUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys                    list providers and whether a key is set")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys set <p> <key>      store an API key (e.g. modelscope, openrouter)")
	fmt.Fprintln(os.Stderr, "  command | ultra-zen keys set <p> -  read an API key from stdin (avoids process arguments)")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys clear <p>          remove a stored key")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys path               print the keys directory")
	fmt.Fprintln(os.Stderr, "  ultra-zen keys --system ...       operate on the machine-wide store (/etc/ultra-zen/keys, needs sudo)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Known providers: "+strings.Join(knownKeyProviders, ", "))
}
