#!/usr/bin/env bash
set -euo pipefail

REPO="github.com/1mr0-tech/tether"
GO_MIN_VERSION="1.22"
INSTALL_DIR="/usr/local/bin"

# ── colours ────────────────────────────────────────────────────────────────────
BOLD=$(tput bold 2>/dev/null || true)
GREEN=$(tput setaf 2 2>/dev/null || true)
CYAN=$(tput setaf 6 2>/dev/null || true)
YELLOW=$(tput setaf 3 2>/dev/null || true)
RED=$(tput setaf 1 2>/dev/null || true)
RESET=$(tput sgr0 2>/dev/null || true)

die()  { echo "${RED}error: $*${RESET}" >&2; exit 1; }
info() { echo "${CYAN}  $*${RESET}"; }
ok()   { echo "${GREEN}  ✓ $*${RESET}"; }

echo ""
echo "${BOLD}${CYAN}  tether — developer installer${RESET}"
echo ""

# ── check / install Go ────────────────────────────────────────────────────────
install_go() {
  info "Go not found — installing Go ${GO_MIN_VERSION}..."

  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64 | amd64)  ARCH="amd64" ;;
    arm64 | aarch64) ARCH="arm64" ;;
    *) die "Unsupported architecture: $ARCH" ;;
  esac

  GO_URL="https://go.dev/dl/go${GO_MIN_VERSION}.${OS}-${ARCH}.tar.gz"
  TMP_GO=$(mktemp -d)

  info "Downloading Go from ${GO_URL}..."
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$GO_URL" -o "${TMP_GO}/go.tar.gz" || die "Failed to download Go"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$GO_URL" -O "${TMP_GO}/go.tar.gz" || die "Failed to download Go"
  else
    die "Neither curl nor wget found — install one and retry"
  fi

  if command -v sudo >/dev/null 2>&1; then
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "${TMP_GO}/go.tar.gz"
  else
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "${TMP_GO}/go.tar.gz"
  fi
  rm -rf "$TMP_GO"

  export PATH="/usr/local/go/bin:$PATH"
  ok "Go installed at /usr/local/go"

  # Persist to shell profile
  PROFILE=""
  if [[ -f "$HOME/.zshrc" ]]; then
    PROFILE="$HOME/.zshrc"
  elif [[ -f "$HOME/.bashrc" ]]; then
    PROFILE="$HOME/.bashrc"
  elif [[ -f "$HOME/.profile" ]]; then
    PROFILE="$HOME/.profile"
  fi
  if [[ -n "$PROFILE" ]] && ! grep -q '/usr/local/go/bin' "$PROFILE"; then
    echo 'export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"' >> "$PROFILE"
    info "Added Go to PATH in ${PROFILE}"
  fi
}

version_ge() {
  # Returns 0 if $1 >= $2 (semver, major.minor only)
  local a_major a_minor b_major b_minor
  a_major=$(echo "$1" | cut -d. -f1)
  a_minor=$(echo "$1" | cut -d. -f2)
  b_major=$(echo "$2" | cut -d. -f1)
  b_minor=$(echo "$2" | cut -d. -f2)
  (( a_major > b_major || (a_major == b_major && a_minor >= b_minor) ))
}

if command -v go >/dev/null 2>&1; then
  GO_VERSION=$(go version | grep -oE '[0-9]+\.[0-9]+' | head -1)
  if version_ge "$GO_VERSION" "$GO_MIN_VERSION"; then
    ok "Go ${GO_VERSION} found"
  else
    echo "${YELLOW}  Go ${GO_VERSION} is below required ${GO_MIN_VERSION} — reinstalling...${RESET}"
    install_go
  fi
else
  install_go
fi

# Ensure GOPATH/bin is in PATH for the install step
export PATH="${GOPATH:-$HOME/go}/bin:/usr/local/go/bin:$PATH"

# ── build and install ─────────────────────────────────────────────────────────
info "Installing tether from ${REPO}..."
go install "${REPO}@latest" 2>&1 || die "go install failed — check your internet connection"
ok "Built and installed via go install"

# The binary lands in $GOPATH/bin (usually ~/go/bin)
GOBIN="${GOPATH:-$HOME/go}/bin"
TETHER_BIN="${GOBIN}/tether"

[[ -f "$TETHER_BIN" ]] || die "Binary not found at ${TETHER_BIN} after install"

# ── move to a system-wide location ───────────────────────────────────────────
if [[ "$TETHER_BIN" != "${INSTALL_DIR}/tether" ]]; then
  if [[ -w "$INSTALL_DIR" ]]; then
    mv "$TETHER_BIN" "${INSTALL_DIR}/tether"
    ok "Moved to ${INSTALL_DIR}/tether"
  elif command -v sudo >/dev/null 2>&1; then
    info "sudo required to install to ${INSTALL_DIR}..."
    sudo mv "$TETHER_BIN" "${INSTALL_DIR}/tether"
    ok "Moved to ${INSTALL_DIR}/tether (via sudo)"
  else
    # Leave it in ~/go/bin and ensure PATH
    INSTALL_DIR="$GOBIN"
    ok "Binary available at ${INSTALL_DIR}/tether"
    if [[ ":$PATH:" != *":${GOBIN}:"* ]]; then
      echo ""
      echo "${YELLOW}  Add this to your shell profile (~/.bashrc or ~/.zshrc):${RESET}"
      echo "    export PATH=\"\$HOME/go/bin:\$PATH\""
    fi
  fi
fi

# ── verify ────────────────────────────────────────────────────────────────────
tether --help >/dev/null 2>&1 || \
  "${INSTALL_DIR}/tether" --help >/dev/null 2>&1 || \
  die "Installed binary failed to run — contact your ops team"

echo ""
echo "${BOLD}${GREEN}  tether is installed!${RESET}"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "${BOLD}  Usage:${RESET}"
echo ""
echo "  Get the session token from your ops team, then run:"
echo ""
echo "  ${BOLD}${YELLOW}tether connect --session <token> --port <your-local-port>${RESET}"
echo ""
echo "  Example — if your app runs on port 3000 locally:"
echo "  ${CYAN}tether connect --session <token> --port 3000${RESET}"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
