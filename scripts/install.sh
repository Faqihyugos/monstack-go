#!/usr/bin/env bash
set -euo pipefail

# MonStack-Go One-Line Installer
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Faqihyugos/monstack-go/main/scripts/install.sh | bash
#   curl -fsSL ... | bash -s -- --component agent

REPO="Faqihyugos/monstack-go"
COMPONENT="all"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/monstack"
DATA_DIR="/var/lib/monstack"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --component) COMPONENT="$2"; shift 2 ;;
    --install-dir) INSTALL_DIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

echo "Installing MonStack-Go (component=${COMPONENT}, os=${OS}, arch=${ARCH})..."

if command -v go >/dev/null 2>&1; then
  TMPDIR="$(mktemp -d)"
  trap 'rm -rf "$TMPDIR"' EXIT
  git clone --depth 1 "https://github.com/${REPO}.git" "$TMPDIR/monstack-go"
  cd "$TMPDIR/monstack-go"
  if [[ "$COMPONENT" == "all" || "$COMPONENT" == "server" ]]; then
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "${INSTALL_DIR}/monstack" ./cmd/monstack
    mkdir -p "$CONFIG_DIR" "$DATA_DIR"
    [[ -f "${CONFIG_DIR}/monstack.yaml" ]] || cp configs/examples/monstack.yaml "${CONFIG_DIR}/monstack.yaml"
    echo "Installed monstack server -> ${INSTALL_DIR}/monstack"
  fi
  if [[ "$COMPONENT" == "all" || "$COMPONENT" == "agent" ]]; then
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "${INSTALL_DIR}/monstack-agent" ./cmd/agent
    echo "Installed monstack-agent -> ${INSTALL_DIR}/monstack-agent"
  fi
  echo "Done! Run: monstack server --config ${CONFIG_DIR}/monstack.yaml"
else
  echo "Go toolchain not found. Please install Go 1.23+ or download prebuilt binaries from https://github.com/${REPO}/releases" >&2
  exit 1
fi
