package e2e

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installScript is the script under test, as a user would curl it.
func installScript() string { return filepath.Join(repoRoot, "install.sh") }

// release builds the artefacts a GitHub release would serve: a tarball
// holding the binary, and the checksums.txt that guards it.
type release struct {
	dir      string
	version  string
	tarball  string // path to the .tar.gz
	tarName  string // its basename, which install.sh constructs and requests
	checksum string // path to checksums.txt
}

// newRelease packages the already-built test binary the way goreleaser does,
// so install.sh's download-verify-extract path runs against real bytes.
func newRelease(t *testing.T, version string) *release {
	t.Helper()
	dir := t.TempDir()
	// install.sh derives this from uname and requests exactly this filename,
	// so the fixture has to be named for the host it is running on.
	platform := runtime.GOOS + "_" + runtime.GOARCH
	name := fmt.Sprintf("ultra-zen_%s_%s.tar.gz", version, platform)
	tarPath := filepath.Join(dir, name)

	bin, err := os.ReadFile(uzBin)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "ultra-zen", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(bin); err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}

	sum := sha256.Sum256(mustRead(t, tarPath))
	checksums := filepath.Join(dir, "checksums.txt")
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	if err := os.WriteFile(checksums, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return &release{dir: dir, version: version, tarball: tarPath, tarName: name, checksum: checksums}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeCurl puts a `curl` on PATH that serves the release from disk instead of
// from github.com.
//
// install.sh hardcodes its URLs, so there is no base-URL knob to point
// somewhere else; shimming the fetcher is the only way to exercise download,
// checksum verification and extraction without the network. The shim matches
// on the tail of the URL, which is exactly the part install.sh constructs.
func (e *env) fakeCurl(rel *release) {
	e.t.Helper()
	script := `#!/bin/sh
# Test shim: serve release artefacts from disk. Mirrors the two call shapes
# install.sh uses: "curl -fsSL <url>" to stdout and "curl -fsSL -o <file> <url>".
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
src=""
case "$url" in
  *checksums.txt) src=` + shellQuote(rel.checksum) + ` ;;
  *` + rel.tarName + `) src=` + shellQuote(rel.tarball) + ` ;;
  *releases/latest) printf '{"tag_name": "%s"}\n' ` + shellQuote(rel.version) + `; exit 0 ;;
esac
[ -n "$src" ] || exit 22
if [ -n "$out" ]; then cp "$src" "$out"; else cat "$src"; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(e.bin, "curl"), []byte(script), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

// cleanPath strips the developer's own PATH down to the system tools plus
// this test's shim directory.
//
// It is load-bearing, not tidiness. install.sh checks whether an ultra-zen is
// already installed and, when its version differs from the release being
// fetched, execs that binary's own updater instead of unpacking the tarball —
// on a pipe it does so without asking. A machine with ultra-zen already on
// PATH (every developer's, and any CI runner that installed it in an earlier
// step) therefore never exercises the download-verify-extract path at all,
// and a fresh-install test there silently tests nothing.
func (e *env) cleanPath() *env {
	return e.set("PATH", strings.Join([]string{e.bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
}

// TestInstallScriptInstallsARelease is the start of the whole flow the user
// asked for: run install.sh as published, end up with a working `uz` on disk.
func TestInstallScriptInstallsARelease(t *testing.T) {
	e := newEnv(t).cleanPath()
	rel := newRelease(t, "v9.9.9-test")
	e.fakeCurl(rel)
	target := filepath.Join(e.home, "install-target")

	r := e.runCmd(launchTimeout, "sh", installScript(), "--dir="+target)
	if r.code != 0 {
		t.Fatalf("install.sh exited %d:\n%s", r.code, r.out())
	}

	installed := filepath.Join(target, "ultra-zen")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("install.sh installed nothing at %s: %v\n%s", installed, err, r.out())
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %o", info.Mode().Perm())
	}
	// It must be the real binary, not a truncated download.
	v := e.runCmd(short, installed, "--version")
	if v.code != 0 || !strings.Contains(v.stdout, "ultra-zen") {
		t.Fatalf("installed binary --version = %q (exit %d)", v.out(), v.code)
	}
	// And the `uz` shorthand the docs tell people to use must exist.
	if _, err := os.Stat(filepath.Join(target, "uz")); err != nil {
		t.Errorf("install.sh created no uz shorthand: %v", err)
	}
}

// TestInstallScriptRefusesATamperedTarball is the security property of the
// install path: the checksum is the only thing standing between a user and a
// substituted binary, so a mismatch must abort rather than warn.
func TestInstallScriptRefusesATamperedTarball(t *testing.T) {
	e := newEnv(t).cleanPath()
	rel := newRelease(t, "v9.9.9-test")
	// Corrupt the tarball AFTER its checksum was recorded.
	if err := os.WriteFile(rel.tarball, []byte("not the binary you were promised"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.fakeCurl(rel)
	target := filepath.Join(e.home, "install-target")

	r := e.runCmd(launchTimeout, "sh", installScript(), "--dir="+target)
	if r.code == 0 {
		t.Fatalf("install.sh accepted a tarball whose checksum did not match:\n%s", r.out())
	}
	if _, err := os.Stat(filepath.Join(target, "ultra-zen")); err == nil {
		t.Error("install.sh installed a binary despite the checksum mismatch")
	}
}

// TestInstallScriptRejectsUnknownOptions keeps a typo from being ignored and
// silently installing with defaults.
func TestInstallScriptRejectsUnknownOptions(t *testing.T) {
	e := newEnv(t)
	r := e.runCmd(short, "sh", installScript(), "--not-a-real-option")
	if r.code == 0 {
		t.Fatalf("install.sh accepted an unknown option:\n%s", r.out())
	}
	requireContains(t, r.out(), "unknown option", "install.sh rejection")
}

// TestInstallScriptUpdateDelegates covers `install.sh --update`, which must
// hand off to the already-installed binary's own updater rather than
// downloading a release itself.
func TestInstallScriptUpdateDelegates(t *testing.T) {
	e := newEnv(t)
	marker := filepath.Join(e.home, "update-was-called")
	stub := "#!/bin/sh\necho \"$@\" > " + shellQuote(marker) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(e.bin, "uz"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	r := e.runCmd(short, "sh", installScript(), "--update")
	if r.code != 0 {
		t.Fatalf("install.sh --update exited %d:\n%s", r.code, r.out())
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("--update did not delegate to the installed binary: %v\n%s", err, r.out())
	}
	if strings.TrimSpace(string(got)) != "update" {
		t.Errorf("delegated with %q, want \"update\"", strings.TrimSpace(string(got)))
	}
}

// TestInstallScriptUpdateWithNothingInstalled states the honest failure: with
// no binary on PATH there is nothing to update, and the script must say so
// rather than half-install.
func TestInstallScriptUpdateWithNothingInstalled(t *testing.T) {
	e := newEnv(t)
	// An empty PATH except this test's own bin dir: no uz, no ultra-zen.
	e.set("PATH", e.bin)

	r := e.runCmd(short, "sh", installScript(), "--update")
	if r.code == 0 {
		t.Fatalf("--update with nothing installed exited 0:\n%s", r.out())
	}
	requireContains(t, strings.ToLower(r.out()), "not found", "--update with nothing installed")
}

// TestInstallScriptIsPOSIXClean keeps the published one-liner runnable under
// a plain /bin/sh, not just bash.
func TestInstallScriptIsPOSIXClean(t *testing.T) {
	e := newEnv(t)
	if r := e.runCmd(short, "sh", "-n", installScript()); r.code != 0 {
		t.Fatalf("install.sh is not POSIX-parseable: %s", r.out())
	}
}
