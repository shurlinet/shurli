#!/usr/bin/env bash
# install-go.sh - Install, update, or remove Go on Linux/macOS.
# Usage: ./install-go.sh [install|update|remove] [version]
#
# Examples:
#   ./install-go.sh install          # Install latest Go
#   ./install-go.sh install 1.24.4   # Install specific version
#   ./install-go.sh update           # Update to latest Go
#   ./install-go.sh remove           # Remove Go entirely

set -euo pipefail

INSTALL_DIR="/usr/local"
GO_DIR="${INSTALL_DIR}/go"
PROFILE_FILE=""

# Detect shell profile file
detect_profile() {
    if [ -f "$HOME/.bashrc" ]; then
        PROFILE_FILE="$HOME/.bashrc"
    elif [ -f "$HOME/.zshrc" ]; then
        PROFILE_FILE="$HOME/.zshrc"
    elif [ -f "$HOME/.profile" ]; then
        PROFILE_FILE="$HOME/.profile"
    fi
}

# Detect OS and architecture, map to Go's naming
detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$os" in
        linux)  GO_OS="linux" ;;
        darwin) GO_OS="darwin" ;;
        *)      echo "Unsupported OS: $os"; exit 1 ;;
    esac

    case "$arch" in
        x86_64|amd64)  GO_ARCH="amd64" ;;
        aarch64|arm64) GO_ARCH="arm64" ;;
        armv7l|armhf)  GO_ARCH="armv6l" ;;
        *)             echo "Unsupported architecture: $arch"; exit 1 ;;
    esac
}

# Fetch latest Go version from golang.org
fetch_latest_version() {
    curl -fsSL "https://go.dev/VERSION?m=text" | head -1 | sed 's/^go//'
}

# Show current installed version (if any)
current_version() {
    if [ -x "${GO_DIR}/bin/go" ]; then
        "${GO_DIR}/bin/go" version | sed -E 's/.*go([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/'
    else
        echo "none"
    fi
}

do_install() {
    local version="$1"
    local current tarball url

    detect_platform
    current="$(current_version)"

    if [ "$version" = "latest" ]; then
        echo "Fetching latest Go version..."
        version="$(fetch_latest_version)"
    fi

    if [ "$current" = "$version" ]; then
        echo "Go ${version} is already installed."
        exit 0
    fi

    tarball="go${version}.${GO_OS}-${GO_ARCH}.tar.gz"
    url="https://go.dev/dl/${tarball}"

    echo "Platform:    ${GO_OS}/${GO_ARCH}"
    echo "Installing:  Go ${version}"
    [ "$current" != "none" ] && echo "Replacing:   Go ${current}"

    # Download to a secure temp directory (not predictable /tmp path)
    local dl_dir
    dl_dir="$(mktemp -d)"
    echo "Downloading: ${url}"
    curl -fSL -o "${dl_dir}/${tarball}" "$url"

    # Verify checksum from Go's official SHA256 file.
    # Use dl.google.com which returns raw hash (go.dev returns HTML redirect).
    echo "Verifying checksum..."
    local sha_url="https://dl.google.com/go/${tarball}.sha256"
    local expected_sum
    expected_sum="$(curl -fsSL "$sha_url" 2>/dev/null | head -1 | tr -d '[:space:]')" || expected_sum=""
    if [ -n "$expected_sum" ]; then
        local actual_sum
        if command -v sha256sum >/dev/null 2>&1; then
            actual_sum="$(sha256sum "${dl_dir}/${tarball}" | awk '{print $1}')"
        elif command -v shasum >/dev/null 2>&1; then
            actual_sum="$(shasum -a 256 "${dl_dir}/${tarball}" | awk '{print $1}')"
        fi
        if [ -n "${actual_sum:-}" ] && [ "$actual_sum" != "$expected_sum" ]; then
            rm -rf "$dl_dir"
            echo "Checksum mismatch! Expected: $expected_sum Got: $actual_sum"
            exit 1
        fi
        echo "Checksum verified."
    else
        echo "Warning: could not fetch checksum. Skipping verification."
    fi

    # Remove old, extract new
    sudo rm -rf "$GO_DIR"
    sudo tar -C "$INSTALL_DIR" -xzf "${dl_dir}/${tarball}"
    rm -rf "$dl_dir"

    # Add to PATH if not already there
    detect_profile
    if [ -n "$PROFILE_FILE" ] && ! grep -q '/usr/local/go/bin' "$PROFILE_FILE" 2>/dev/null; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> "$PROFILE_FILE"
        echo "Added /usr/local/go/bin to PATH in ${PROFILE_FILE}"
    fi

    echo ""
    "${GO_DIR}/bin/go" version
    echo "Done."
}

do_remove() {
    if [ ! -d "$GO_DIR" ]; then
        echo "Go is not installed at ${GO_DIR}."
        exit 0
    fi

    local current
    current="$(current_version)"
    echo "Removing Go ${current} from ${GO_DIR}..."
    sudo rm -rf "$GO_DIR"

    detect_profile
    if [ -n "$PROFILE_FILE" ]; then
        # Remove the PATH line we added
        sed -i.bak '\|/usr/local/go/bin|d' "$PROFILE_FILE"
        rm -f "${PROFILE_FILE}.bak"
        echo "Removed /usr/local/go/bin from PATH in ${PROFILE_FILE}"
    fi

    echo "Done. Go removed."
}

# Main
ACTION="${1:-install}"
VERSION="${2:-latest}"

case "$ACTION" in
    install|update)
        do_install "$VERSION"
        ;;
    remove|uninstall)
        do_remove
        ;;
    version)
        echo "Installed: Go $(current_version)"
        echo "Latest:    Go $(fetch_latest_version)"
        ;;
    *)
        echo "Usage: $0 [install|update|remove|version] [version]"
        exit 1
        ;;
esac
