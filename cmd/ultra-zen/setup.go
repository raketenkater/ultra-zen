// Setup subcommand: install ultra-zen system-wide and initialise the shared
// key store so any user on the machine can launch it. `ultra-zen setup`
// resolves the running binary, copies it (plus a `uz` symlink) into a bin dir,
// and creates /etc/ultra-zen/keys. `uz` is the on-PATH name; Claude Code stays
// in the directory it was launched from (ultra-zen never chdirs — the exec'd
// claude inherits the launch cwd).
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/raketenkater/ultra-zen/internal/keys"
)

// setupDefaults mirrors install.sh's find_bindir precedence so the Go path and
// the curl-pipe path agree on where the binary lands.
func setupFindBindir(override string) (string, bool) {
	if override != "" {
		return override, false
	}
	// Try /usr/local/bin first (may need sudo).
	if d, err := os.Stat("/usr/local/bin"); err == nil && d.IsDir() {
		if writable, _ := isWritable("/usr/local/bin"); writable {
			return "/usr/local/bin", false
		}
		return "/usr/local/bin", true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "bin"), false
}

// isWritable reports whether dir is writable by the current user.
func isWritable(dir string) (bool, error) {
	f, err := os.CreateTemp(dir, ".ultra-zen-write-test-*")
	if err != nil {
		return false, err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true, nil
}

// setupInstallBinary copies the running binary to dst and makes it executable.
func setupInstallBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// setupCreateSymlink links linkPath to binPath (a relative or absolute link).
// It refuses to overwrite an existing entry that is not already this symlink.
func setupCreateSymlink(binPath, linkPath string) error {
	if cur, err := os.Readlink(linkPath); err == nil && cur == binPath {
		return nil // already the right link
	}
	if _, err := os.Lstat(linkPath); err == nil {
		// Don't clobber an unrelated file or symlink.
		return fmt.Errorf("refusing to replace existing %s (not our symlink)", linkPath)
	}
	return os.Symlink(binPath, linkPath)
}

// ensureUZSymlink is the self-healing install path: whenever the binary runs
// from a directory it can write to (an install dir like ~/.local/bin, not a
// read-only system dir), it makes sure a `uz` symlink exists next to itself.
// This covers `go install`/`make install` and curl-pipe installs, which only
// place the binary and would otherwise leave `uz` missing. Best-effort: any
// failure is silently ignored — a launch must never block on this.
func ensureUZSymlink() {
	if filepath.Base(os.Args[0]) == "uz" {
		return // already running as uz
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	// Only self-heal in writable install dirs. /usr/local/bin and similar
	// root-owned dirs are the `setup` command's job (with sudo).
	if w, _ := isWritable(dir); !w {
		return
	}
	// Don't clobber a `uz` that already points somewhere sensible.
	if cur, err := os.Readlink(filepath.Join(dir, "uz")); err == nil && cur != filepath.Base(exe) {
		return
	}
	_ = setupCreateSymlink(filepath.Base(exe), filepath.Join(dir, "uz"))
}

// systemSetupDone reports whether the system-wide store exists. When it does,
// any local user can already use ultra-zen, so no setup prompt is needed.
func systemSetupDone() bool {
	if st, err := os.Stat(keys.SystemDir()); err == nil && st.IsDir() {
		return true
	}
	return false
}

// promptSystemSetup asks the user whether to set up system-wide access (uz +
// shared keys for all users) and, on yes, re-execs this binary under sudo to
// run `ultra-zen setup --copy-keys`. sudo prompts for the password itself.
// Returns true if setup was run (successfully or not); false if the user
// declined or the prompt is impossible (non-interactive stdin).
func promptSystemSetup(exe string) bool {
	if os.Geteuid() == 0 {
		return false // already root; no sudo needed
	}
	// Only prompt on a real terminal. Without one (scripts, --list) we stay
	// silent — a launch must never hang waiting for input that can't come.
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if !systemSetupDone() {
		fmt.Fprint(os.Stderr, "\nultra-zen: system-wide access isn't set up yet (uz + shared keys for all users).\n")
		fmt.Fprint(os.Stderr, "Set it up now (requires sudo)? [y/N] ")
		var ans string
		if _, err := fmt.Fscanln(os.Stdin, &ans); err != nil {
			return false
		}
		if strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
			fmt.Fprintf(os.Stderr, "\nRunning: sudo %s setup --copy-keys\n", exe)
			cmd := exec.Command("sudo", exe, "setup", "--copy-keys")
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			_ = cmd.Run()
			return true
		}
	}
	return false
}

// setupInitSystemStore creates the system key store directory with 0711 so any
// local user can traverse it but only root can write.
func setupInitSystemStore() error {
	dir := keys.SystemDir()
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return err
	}
	return os.Chmod(dir, 0o711)
}

// setupCopyUserKeys copies every non-empty per-user key into the system store.
// Providers that have no user key are skipped.
func setupCopyUserKeys() (copied []string) {
	for _, p := range knownKeyProviders {
		k := keys.LoadFrom(p, keys.StoreUser)
		if k == "" {
			continue
		}
		if err := keys.SaveSystem(p, k); err != nil {
			fmt.Fprintf(os.Stderr, "ultra-zen: could not copy %s key to system store: %v\n", p, err)
			continue
		}
		copied = append(copied, p)
	}
	return copied
}

// cmdSetup is the `ultra-zen setup` entry point. Non-interactive; prints a
// status report via the stdout var so tests can capture it.
func cmdSetup(args []string) {
	var bindirOverride string
	var copyKeys bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 < len(args) {
				bindirOverride = args[i+1]
				i++
			}
		case "--copy-keys":
			copyKeys = true
		case "-h", "--help", "help":
			setupUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "ultra-zen: unrecognized setup option %q\n", args[i])
			setupUsage()
			os.Exit(1)
		}
	}

	exe, err := os.Executable()
	if err != nil {
		die(fmt.Errorf("resolve running binary: %w", err))
	}
	exe, _ = filepath.Abs(exe)

	binDir, needsSudo := setupFindBindir(bindirOverride)
	if needsSudo && os.Geteuid() != 0 {
		die(fmt.Errorf("/usr/local/bin is not writable; run setup with sudo:\n  sudo ultra-zen setup %s", flagWord(bindirOverride)))
	}
	if err := setupInstallBinary(exe, filepath.Join(binDir, "ultra-zen")); err != nil {
		die(fmt.Errorf("install binary to %s: %w", binDir, err))
	}
	uzPath := filepath.Join(binDir, "uz")
	if err := setupCreateSymlink("ultra-zen", uzPath); err != nil {
		// Non-fatal: a conflicting `uz` exists; the binary is still installed.
		fmt.Fprintf(os.Stderr, "ultra-zen: warning: %v\n", err)
	}
	if err := setupInitSystemStore(); err != nil {
		// Non-fatal when not root: report the hint instead of failing.
		if os.Geteuid() != 0 {
			fmt.Fprintf(os.Stderr, "ultra-zen: could not create system key store %s: %v\n", keys.SystemDir(), err)
			fmt.Fprintf(os.Stderr, "ultra-zen: run `sudo ultra-zen setup --copy-keys` to share keys across users\n")
		} else {
			die(fmt.Errorf("create system key store: %w", err))
		}
	} else if copyKeys {
		copied := setupCopyUserKeys()
		if len(copied) > 0 {
			fmt.Fprintf(stdout, "copied %d key(s) to the system store: %s\n", len(copied), strings.Join(copied, ", "))
		} else {
			fmt.Fprintln(stdout, "no per-user keys to copy to the system store")
		}
	}

	reportSetup(binDir, uzPath, needsSudo, copyKeys)
}

// flagWord renders the --dir override for error messages.
func flagWord(override string) string {
	if override == "" {
		return ""
	}
	return "--dir " + override
}

// reportSetup prints where things landed and what the user should run next.
func reportSetup(binDir, uzPath string, needsSudo, copyKeys bool) {
	fmt.Fprintf(stdout, "\nultra-zen installed to %s/ultra-zen\n", binDir)
	fmt.Fprintf(stdout, "uz -> %s/ultra-zen (symlink)\n", binDir)
	if needsSudo {
		fmt.Fprintf(stdout, "note: installed with sudo; %s is root-owned\n", binDir)
	}
	// Warn if the `uz` that would run on PATH isn't the one just created.
	if !needsSudo {
		if w, err := exec.LookPath("uz"); err == nil && w != uzPath {
			fmt.Fprintf(os.Stderr, "ultra-zen: warning: `uz` on PATH (%s) differs from the installed %s\n", w, uzPath)
		}
	}
	fmt.Fprintf(stdout, "system key store: %s (world-readable 0644; root-writable)\n", keys.SystemDir())
	fmt.Fprintf(stdout, "\nNext steps:\n")
	fmt.Fprintf(stdout, "  uz --list                          # models from any directory\n")
	if !copyKeys {
		fmt.Fprintf(stdout, "  sudo ultra-zen setup --copy-keys  # share your API keys with all users\n")
	}
	fmt.Fprintf(stdout, "  sudo ultra-zen keys --system set <provider> <key>   # set a shared key\n")
}

func setupUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  ultra-zen setup                   install binary + uz symlink to a bin dir")
	fmt.Fprintln(os.Stderr, "  ultra-zen setup --copy-keys       also copy your API keys to /etc/ultra-zen/keys")
	fmt.Fprintln(os.Stderr, "  ultra-zen setup --dir <dir>       install to a specific directory")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Run with sudo when installing to /usr/local/bin.")
}
