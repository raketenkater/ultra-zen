#!/usr/bin/env bash
set -euo pipefail

# ultra-zen installer — single-command setup
# Usage: curl -fsSL https://raw.githubusercontent.com/raketenkater/ultra-zen/master/install.sh | sh

# Clean up the temp dir on any exit. TMPDIR is a global (set during main) so
# this fires reliably even after main() returns — referencing an unset local
# under `set -u` would error on every exit.
cleanup() {
    if [ "${TMPDIR_SET:-false}" = true ]; then
        rm -rf "$TMPDIR"
    fi
}
trap cleanup EXIT

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Configuration
REPO="raketenkater/ultra-zen"
BINARY="ultra-zen"
BINDIR="${ULTRA_ZEN_BINDIR:-}"
VERSION="${ULTRA_ZEN_VERSION:-latest}"
INSTALL_DIR=""
NEEDS_SUDO=false
# Global (not function-local) so the EXIT trap below can reference it even
# after main() returns; under `set -u` a trap on an unset local would error.
TMPDIR=""
TMPDIR_SET=false

log_info()  { echo -e "${GREEN}→${NC} $1"; }
log_warn()  { echo -e "${YELLOW}!${NC} $1"; }
log_error() { echo -e "${RED}✗${NC} $1"; }
log_step()  { echo -e "${CYAN}==>${NC} $1"; }

# --- Platform detection ---
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Linux)  os="linux" ;;
        Darwin) os="darwin" ;;
        *)
            log_error "Unsupported OS: $(uname -s)"
            log_info "ultra-zen supports Linux and macOS."
            log_info "Build from source: go install github.com/raketenkater/ultra-zen/cmd/ultra-zen@latest"
            exit 1
            ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *)
            log_error "Unsupported architecture: $(uname -m)"
            log_info "Build from source: go install github.com/raketenkater/ultra-zen/cmd/ultra-zen@latest"
            exit 1
            ;;
    esac

    echo "${os}_${arch}"
}

# --- Find a writable install directory ---
find_bindir() {
    # User override
    if [ -n "$BINDIR" ]; then
        INSTALL_DIR="$BINDIR"
        return
    fi

    # Try /usr/local/bin first (may need sudo)
    if [ -d "/usr/local/bin" ] && [ -w "/usr/local/bin" ]; then
        INSTALL_DIR="/usr/local/bin"
    elif [ -d "/usr/local/bin" ]; then
        INSTALL_DIR="/usr/local/bin"
        NEEDS_SUDO=true
    elif [ -d "$HOME/.local/bin" ]; then
        INSTALL_DIR="$HOME/.local/bin"
    else
        INSTALL_DIR="$HOME/.local/bin"
        mkdir -p "$INSTALL_DIR"
    fi
}

# --- Resolve latest version ---
resolve_version() {
    if [ "$VERSION" != "latest" ]; then
        echo "$VERSION"
        return
    fi
    local tag
    tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | \
        grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"\(.*\)"/\1/')
    if [ -z "$tag" ]; then
        log_error "Could not determine latest release version."
        log_info "Try pinning a version: curl -fsSL ... | ULTRA_ZEN_VERSION=v0.1.0 sh"
        log_info "Or build from source: go install github.com/raketenkater/ultra-zen/cmd/ultra-zen@latest"
        exit 1
    fi
    echo "$tag"
}

# --- Main ---
main() {
    log_step "ultra-zen installer"

    local platform version tarball url

    platform=$(detect_platform)
    version=$(resolve_version)
    log_info "Platform: ${platform}, Version: ${version}"

    find_bindir
    log_info "Install directory: ${INSTALL_DIR}"

    # Download URL
    tarball="${BINARY}_${version}_${platform}.tar.gz"
    url="https://github.com/${REPO}/releases/download/${version}/${tarball}"

    # Create temp dir. The temp path is stored in globals so the EXIT trap can
    # remove it even after main() returns; the trap itself is installed once at
    # the top of the script (see below).
    TMPDIR=$(mktemp -d)
    TMPDIR_SET=true

    # Download
    log_step "Downloading ${tarball}..."
    if ! curl -fsSL --progress-bar -o "${TMPDIR}/${tarball}" "$url"; then
        log_error "Download failed: ${url}"
        log_info "The release may not yet have binaries for your platform."
        log_info "Build from source: go install github.com/raketenkater/ultra-zen/cmd/ultra-zen@latest"
        exit 1
    fi

    # Extract
    log_step "Extracting..."
    tar -xzf "${TMPDIR}/${tarball}" -C "${TMPDIR}"

    # Verify binary exists
    if [ ! -f "${TMPDIR}/${BINARY}" ]; then
        log_error "Binary not found in archive. Contents:"
        ls -la "${TMPDIR}/"
        exit 1
    fi

    # Install
    log_step "Installing to ${INSTALL_DIR}..."
    if [ "$NEEDS_SUDO" = true ]; then
        log_info "Need sudo for ${INSTALL_DIR}"
        sudo cp "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
        sudo chmod +x "${INSTALL_DIR}/${BINARY}"
    else
        cp "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
        chmod +x "${INSTALL_DIR}/${BINARY}"
    fi

    # Verify installation
    if ! command -v "${BINARY}" >/dev/null 2>&1 && ! [ -x "${INSTALL_DIR}/${BINARY}" ]; then
        log_warn "${BINARY} installed but may not be on PATH."
        if [ "$INSTALL_DIR" = "$HOME/.local/bin" ]; then
            log_info "Add to your shell config:"
            echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
        fi
    fi

    # Final check
    if "${INSTALL_DIR}/${BINARY}" --version >/dev/null 2>&1; then
        log_info "$(${INSTALL_DIR}/${BINARY} --version)"
        log_info "ultra-zen installed to ${INSTALL_DIR}/${BINARY}"
    else
        log_warn "Binary may not be functional: ${INSTALL_DIR}/${BINARY} --version"
    fi

    # --- Claude Code detection ---
    echo ""
    log_step "Checking prerequisites..."

    local node_needed=false
    local claude_installed=true

    if ! command -v node >/dev/null 2>&1; then
        node_needed=true
    fi

    if ! command -v claude >/dev/null 2>&1; then
        claude_installed=false
    fi

    if [ "$node_needed" = true ]; then
        log_warn "Node.js is not installed. Claude Code requires Node.js >= 18."
        log_info "Install Node.js: https://nodejs.org"
        echo ""
    fi

    if [ "$claude_installed" = false ]; then
        log_warn "Claude Code is not installed or not on PATH."
        if [ "$node_needed" = false ]; then
            log_info "Install Claude Code:"
            echo ""
            echo "  npm install -g @anthropic-ai/claude-code"
            echo ""
        else
            log_info "After installing Node.js, run:"
            echo ""
            echo "  npm install -g @anthropic-ai/claude-code"
            echo ""
        fi
    else
        log_info "Claude Code found: $(claude --version 2>/dev/null || echo 'installed')"
    fi

    # --- uvx check (for web research) ---
    if ! command -v uvx >/dev/null 2>&1; then
        log_warn "uvx not found — web research (DDG MCP) will be unavailable."
        log_info "Install: curl -LsSf https://astral.sh/uv/install.sh | sh"
    fi

    echo ""
    log_step "Next steps:"
    if [ "$claude_installed" = false ]; then
        echo "  Install Claude Code (see above), then run: ultra-zen"
    else
        echo "  ultra-zen"
    fi
    echo ""
    echo "  With OpenRouter:  OPENROUTER_API_KEY=sk-or-v1-... ultra-zen --provider openrouter"
    echo "  With worker split: ultra-zen glm-5.1 --worker mini-max-m2.5"
    echo "  Docs: https://github.com/raketenkater/ultra-zen"
}

main "$@"
