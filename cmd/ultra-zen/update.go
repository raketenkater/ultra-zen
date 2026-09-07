// Update subcommand: upgrade the running binary to the latest release from
// GitHub. `ultra-zen update` resolves the running binary, downloads the
// matching platform tarball, verifies its checksum, and installs atomically.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	updateRepo   = "raketenkater/ultra-zen"
	updateBinary = "ultra-zen"
)

// release is the JSON structure returned by the GitHub Releases API.
type release struct {
	TagName string `json:"tag_name"`
}

// cmdUpdate is the `ultra-zen update` entry point.
func cmdUpdate(args []string) {
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stderr, "Usage:")
			fmt.Fprintln(os.Stderr, "  ultra-zen update           upgrade to the latest release")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "  ultra-zen update --version X.Y.Z  update to a specific version")
			os.Exit(0)
		case "--version":
			if len(args) >= 2 {
				updateVersion(args[1])
				return
			}
			fmt.Fprintf(os.Stderr, "ultra-zen: --version requires a version argument\n")
			os.Exit(1)
		default:
			fmt.Fprintf(os.Stderr, "ultra-zen: unrecognized update option %q\n", a)
			os.Exit(1)
		}
	}
	updateLatest()
}

// updateVersion upgrades to a specific version instead of the latest release.
func updateVersion(tag string) {
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if err := doUpdate(tag); err != nil {
		die(fmt.Errorf("update to %s: %w", tag, err))
	}
}

// updateLatest finds the latest release and upgrades to it.
func updateLatest() {
	client := &http.Client{Timeout: 30 * time.Second}
	ver, err := fetchLatestVersion(client)
	if err != nil {
		die(fmt.Errorf("resolve latest release: %w", err))
	}
	// Normalize: compare without leading "v" prefix.
	remote := strings.TrimPrefix(ver, "v")
	if remote == Version {
		fmt.Fprintln(stdout, "already up to date (" + Version + ")")
		return
	}
	if err := doUpdate(ver); err != nil {
		die(fmt.Errorf("update to %s: %w", ver, err))
	}
}

// fetchLatestVersion fetches the latest release tag from GitHub.
func fetchLatestVersion(client *http.Client) (string, error) {
	resp, err := client.Get("https://api.github.com/repos/" + updateRepo + "/releases/latest")
	if err != nil {
		return "", fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitHub API %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var r release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("parse release JSON: %w", err)
	}
	if r.TagName == "" {
		return "", errors.New("no tag_name in release response")
	}
	return r.TagName, nil
}

// detectPlatform returns the GOOS/GOARCH-based platform string that matches
// the release tarball naming convention (e.g. "linux_amd64").
func detectPlatform() string {
	return runtime.GOOS + "_" + runtime.GOARCH
}

// downloadAndExtract downloads the release tarball, verifies its checksum
// (warning-only if checksums.txt is missing), and extracts the ultra-zen
// binary to tmpDir. Returns the path to the extracted binary.
func downloadAndExtract(client *http.Client, version, platform, tmpDir string) (string, error) {
	tarballName := fmt.Sprintf("%s_%s_%s.tar.gz", updateBinary, version, platform)
	tarballURL := "https://github.com/" + updateRepo + "/releases/download/" + version + "/" + tarballName

	// Download tarball, computing SHA-256 on the fly so we can verify against
	// the published checksums.txt (which is the SHA-256 of the .tar.gz itself).
	resp, err := client.Get(tarballURL)
	if err != nil {
		return "", fmt.Errorf("download tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("tarball HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	h := sha256.New()
	tarballData, err := io.ReadAll(io.TeeReader(resp.Body, h))
	if err != nil {
		return "", fmt.Errorf("read tarball: %w", err)
	}

	// Verify the SHA-256 of the downloaded tarball against checksums.txt.
	wantSHA := fetchChecksum(client, version, tarballName)
	if wantSHA != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
			return "", fmt.Errorf("checksum mismatch: got %s, want %s", got, wantSHA)
		}
		fmt.Fprintf(stdout, "checksum ok: %s\n", wantSHA)
	}

	// Decompress gzip → tar.
	gr, err := gzip.NewReader(bytes.NewReader(tarballData))
	if err != nil {
		return "", fmt.Errorf("decompress tarball: %w", err)
	}
	tr := tar.NewReader(gr)

	// Find the binary anywhere in the archive. goreleaser wraps the binary in
	// a directory whose name varies by format, so match on the basename only.
	var extracted string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			gr.Close()
			return "", fmt.Errorf("tar read: %w", err)
		}
		if filepath.Base(hdr.Name) != updateBinary || hdr.Typeflag != tar.TypeReg {
			continue
		}
		extracted = filepath.Join(tmpDir, updateBinary)
		out, err := os.OpenFile(extracted, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			gr.Close()
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			gr.Close()
			out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			gr.Close()
			return "", err
		}
		break
	}
	gr.Close()
	if extracted == "" {
		return "", fmt.Errorf("binary %s not found in tarball", updateBinary)
	}
	return extracted, nil
}

// fetchChecksum downloads checksums.txt from the release and returns the
// SHA-256 for the given tarball, or "" if the file is absent or doesn't
// contain an entry.
func fetchChecksum(client *http.Client, version, tarballName string) string {
	cksumURL := "https://github.com/" + updateRepo + "/releases/download/" + version + "/checksums.txt"
	cksumResp, err := client.Get(cksumURL)
	if err == nil && cksumResp.StatusCode == http.StatusOK {
		cksumBody, _ := io.ReadAll(cksumResp.Body)
		cksumResp.Body.Close()
		for _, line := range strings.Split(strings.TrimSpace(string(cksumBody)), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[1] == tarballName {
				return parts[0]
			}
		}
	}
	return ""
}

// doUpdate installs the downloaded binary into the same directory as the
// currently running ultra-zen binary.
func doUpdate(version string) error {
	fmt.Fprintf(stdout, "updating to %s…\n", version)

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve running binary: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	binDir := filepath.Dir(exe)
	binPath := filepath.Join(binDir, updateBinary)

	platform := detectPlatform()
	tmpDir, err := os.MkdirTemp("", "uz-update-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	client := &http.Client{Timeout: 30 * time.Second}
	dlBin, err := downloadAndExtract(client, version, platform, tmpDir)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "installing %s…\n", dlBin)

	// Check if install directory is writable.
	writable, _ := isWritable(binDir)
	if writable {
		if err := setupInstallBinary(dlBin, binPath); err != nil {
			return fmt.Errorf("install binary: %w", err)
		}
	} else {
		// Need sudo.
		fmt.Fprintf(stdout, "%s is not writable; using sudo…\n", binDir)
		sudoTmp := filepath.Join(tmpDir, "ultra-zen.sudo")
		if err := setupInstallBinary(dlBin, sudoTmp); err != nil {
			return fmt.Errorf("stage sudo binary: %w", err)
		}
		cmdOut, err := exec.Command("sudo", "mv", sudoTmp, binPath).CombinedOutput()
		if err != nil {
			os.Remove(sudoTmp)
			return fmt.Errorf("sudo mv: %s: %w", string(cmdOut), err)
		}
	}

	// Ensure uz symlink.
	uzPath := filepath.Join(binDir, "uz")
	if err := setupCreateSymlink(updateBinary, uzPath); err != nil {
		fmt.Fprintf(os.Stderr, "ultra-zen: warning: %v\n", err)
	}

	// Verify the new binary runs.
	if out, err := exec.Command(binPath, "--version").CombinedOutput(); err != nil {
		return fmt.Errorf("verify new binary: %s: %w", string(out), err)
	}

	fmt.Fprintf(stdout, "updated to %s (platform: %s)\n", version, platform)
	return nil
}
