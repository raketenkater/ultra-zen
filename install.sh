#!/bin/sh
# ultra-zen installer — POSIX sh (no bash required, works under dash/busybox).
# Usage:  curl -fsSL https://raw.githubusercontent.com/raketenkater/ultra-zen/master/install.sh | sh
# Args:   sh -s -- --system        install system-wide to /usr/local/bin via sudo
#         sh -s -- --dir=<dir>     install into <dir>
# Env:    ULTRA_ZEN_VERSION=v0.2.1 pin a version (default: latest release)
#         ULTRA_ZEN_BINDIR=<dir>   install target (default ~/.local/bin)
#         ULTRA_ZEN_SYSTEM=1       same as --system
#         ULTRA_ZEN_ADD_PATH=1     consent to append the PATH export to your
#                                  shell config non-interactively (a piped run
#                                  never prompts and never writes without it)
set -eu

REPO="raketenkater/ultra-zen"
BINARY="ultra-zen"
BINDIR="${ULTRA_ZEN_BINDIR:-}"
VERSION="${ULTRA_ZEN_VERSION:-}"
SYSTEM="${ULTRA_ZEN_SYSTEM:-}"
ADD_PATH="${ULTRA_ZEN_ADD_PATH:-}"

log()  { printf '→ %s\n' "$1"; }
warn() { printf '! %s\n' "$1"; }
die()  { printf '✗ %s\n' "$1" >&2; exit 1; }
confirm() { printf '%s' "$1"; read -r ans || return 1; [ "$ans" = y ] || [ "$ans" = Y ] || [ "$ans" = yes ]; }

for arg in ${1+"$@"}; do
  case "$arg" in
    --system)   SYSTEM=1 ;;
    --dir=*)    BINDIR="${arg#--dir=}" ;;
    --add-path) ADD_PATH=1 ;;
    *) die "unknown option: $arg (supported: --system, --dir=<dir>, --add-path)" ;;
  esac
done

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64|Linux-amd64)   platform=linux_amd64 ;;
  Linux-aarch64|Linux-arm64)  platform=linux_arm64 ;;
  Darwin-x86_64)              platform=darwin_amd64 ;;
  Darwin-arm64)               platform=darwin_arm64 ;;
  *) die "unsupported platform $(uname -s)-$(uname -m); build from source: go install github.com/$REPO/cmd/ultra-zen@latest" ;;
esac

if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1 || true)
  [ -n "$VERSION" ] || die "could not resolve the latest release; pin one: ULTRA_ZEN_VERSION=v0.2.1 curl ... | sh"
fi

if [ -z "$BINDIR" ]; then
  if [ "$SYSTEM" = "1" ]; then BINDIR=/usr/local/bin; else BINDIR="$HOME/.local/bin"; fi
fi

tarball="${BINARY}_${VERSION}_${platform}.tar.gz"
log "installing ${VERSION} (${platform}) into ${BINDIR}"
tmp=$(mktemp -d) || die "mktemp failed"
trap 'rm -rf "$tmp"' EXIT INT TERM

curl -fsSL -o "$tmp/$tarball" "https://github.com/$REPO/releases/download/$VERSION/$tarball" \
  || die "download failed; check https://github.com/$REPO/releases for published binaries"

# Verify the checksum when the release publishes checksums.txt (goreleaser
# does); a release without one is only a warning, never a hard failure.
if curl -fsSL -o "$tmp/checksums.txt" "https://github.com/$REPO/releases/download/$VERSION/checksums.txt" 2>/dev/null; then
  want=$(grep " $tarball\$" "$tmp/checksums.txt" | awk '{print $1}' | head -n 1 || true)
  if [ -n "$want" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      echo "$want  $tmp/$tarball" | sha256sum -c - >/dev/null 2>&1 || die "checksum mismatch for $tarball"
    elif command -v shasum >/dev/null 2>&1; then
      echo "$want  $tmp/$tarball" | shasum -a 256 -c - >/dev/null 2>&1 || die "checksum mismatch for $tarball"
    else
      warn "no sha256 tool found; skipping checksum verification"
    fi
  fi
else
  warn "release $VERSION publishes no checksums.txt; skipping verification"
fi

tar -xzf "$tmp/$tarball" -C "$tmp"
[ -f "$tmp/$BINARY" ] || die "archive did not contain $BINARY"

mkdir -p "$BINDIR" || die "cannot create $BINDIR"
if [ -w "$BINDIR" ]; then
  cp "$tmp/$BINARY" "$BINDIR/"
  chmod 0755 "$BINDIR/$BINARY"
else
  log "$BINDIR is not writable; using sudo (password prompt on your terminal)"
  sudo cp "$tmp/$BINARY" "$BINDIR/" || die "sudo install failed"
  sudo chmod 0755 "$BINDIR/$BINARY"
fi
ln -sf "$BINARY" "$BINDIR/uz" 2>/dev/null || sudo ln -sf "$BINARY" "$BINDIR/uz"

"$BINDIR/$BINARY" --version 2>/dev/null || warn "$BINDIR/$BINARY did not report a version"

command -v claude >/dev/null 2>&1 \
  || warn "Claude Code is not on PATH; install it: npm install -g @anthropic-ai/claude-code"

case ":$PATH:" in *":$BINDIR:"*) : ;; *)
  line="export PATH=\"$BINDIR:\$PATH\""
  warn "$BINDIR is not on your PATH"
  printf '  add it:  %s\n' "$line"
  # Only write shell config with consent: an interactive y, or the explicit
  # ULTRA_ZEN_ADD_PATH=1 for piped runs. Never silently.
  if [ "$ADD_PATH" = 1 ] || { [ -t 0 ] && confirm "write this line into your shell config now? [y/N] "; }; then
    rc="$HOME/.profile"
    case "${SHELL##*/}" in zsh) rc="$HOME/.zshrc" ;; esac
    [ -f "$rc" ] || rc="$HOME/.bashrc"
    if [ -f "$rc" ] && grep -qF "$BINDIR" "$rc"; then
      log "$rc already mentions $BINDIR; not touching it"
    else
      printf '\n# added by ultra-zen install\n%s\n' "$line" >> "$rc" \
        && log "written to $rc — open a new shell or run: $line"
    fi
  else
    warn "PATH not updated (a piped run never writes without ULTRA_ZEN_ADD_PATH=1)"
  fi
  ;;
esac

log "done. next steps:"
echo "  uz setup providers   # store provider API keys (opencode Zen, OpenRouter, ...)"
echo "  uz                   # pick a model and launch Claude Code"

