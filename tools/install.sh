#!/bin/sh
# install.sh - Download or build Shurli, then set up as peer node or relay server.
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/shurlinet/shurli/dev/tools/install.sh | sh
#   curl -sSL https://raw.githubusercontent.com/shurlinet/shurli/dev/tools/install.sh | sh -s -- --version v0.2.0
#
# Options (passed after --):
#   --version VERSION   Install a specific version (default: latest release)
#   --method METHOD     Install method: "download" or "build" (default: interactive)
#   --role ROLE         Setup role: "peer", "relay", or "binary" (default: interactive)
#   --dir DIR           Install directory (default: /usr/local/bin)
#   --no-verify         Skip SHA256 checksum verification (download only)
#   --help              Show this help

set -eu

REPO="shurlinet/shurli"
REPO_URL="https://github.com/${REPO}"
BINARY_NAME="shurli"
INSTALL_DIR="/usr/local/bin"
USER_INSTALL_DIR="${HOME}/.local/bin"

# --- Output helpers ---

log()   { printf '  %s\n' "$*"; }
info()  { printf '\n  \033[1;34m>\033[0m %s\n' "$*"; }
warn()  { printf '  \033[1;33mWarning:\033[0m %s\n' "$*"; }
error() { printf '\n  \033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }
bold()  { printf '  \033[1m%s\033[0m\n' "$*"; }

# --- System helpers ---

has_cmd() { command -v "$1" >/dev/null 2>&1; }

# Run with sudo only when not root
run_sudo() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

# Prompt user for a choice. Returns the entered value.
# Usage: result=$(prompt "Choice [1]: " "1")
prompt() {
    local msg="$1" default="${2:-}"
    printf '  %s' "$msg" >&2
    read -r REPLY </dev/tty 2>/dev/null || REPLY="$default"
    REPLY="${REPLY:-$default}"
    printf '%s' "$REPLY"
}

# --- Platform detection ---

detect_platform() {
    OS="$(uname -s)"
    ARCH="$(uname -m)"

    case "$OS" in
        Linux)  GOOS="linux" ;;
        Darwin) GOOS="darwin" ;;
        MINGW*|MSYS*|CYGWIN*) GOOS="windows" ;;
        *) error "Unsupported OS: $OS" ;;
    esac

    case "$ARCH" in
        x86_64|amd64)   GOARCH="amd64" ;;
        aarch64|arm64)  GOARCH="arm64" ;;
        *) error "Unsupported architecture: $ARCH" ;;
    esac

    SUFFIX=""
    [ "$GOOS" = "windows" ] && SUFFIX=".exe"
}

# --- Download helper ---

download() {
    local url="$1" dest="$2"
    if has_cmd curl; then
        curl -fsSL -o "$dest" "$url" || return 1
    elif has_cmd wget; then
        wget -qO "$dest" "$url" || return 1
    else
        error "Either curl or wget is required."
    fi
}

# --- Version resolution ---

fetch_latest_version() {
    local url="https://api.github.com/repos/${REPO}/releases/latest"
    local response
    if has_cmd curl; then
        response="$(curl -fsSL "$url" 2>/dev/null)" || error "Failed to fetch latest version. Check your network connection."
    elif has_cmd wget; then
        response="$(wget -qO- "$url" 2>/dev/null)" || error "Failed to fetch latest version. Check your network connection."
    fi
    VERSION="$(printf '%s' "$response" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"//' | sed 's/".*//')"
    [ -z "$VERSION" ] && error "No releases found at ${REPO_URL}/releases"
}

# --- Upgrade detection ---

check_existing_install() {
    EXISTING_VERSION=""
    EXISTING_PATH=""

    # Check common install locations
    for dir in "$INSTALL_DIR" "$USER_INSTALL_DIR"; do
        if [ -x "${dir}/${BINARY_NAME}" ]; then
            EXISTING_PATH="${dir}/${BINARY_NAME}"
            EXISTING_VERSION="$("$EXISTING_PATH" --version 2>/dev/null | head -1 | awk '{print $2}')" || EXISTING_VERSION="unknown"
            break
        fi
    done
}

handle_existing_install() {
    if [ -z "$EXISTING_PATH" ]; then
        return 0
    fi

    info "Existing installation detected"
    log "Path:    $EXISTING_PATH"
    log "Version: $EXISTING_VERSION"
    log "Target:  $VERSION"
    printf '\n'

    local choice
    choice=$(prompt "  1) Upgrade (replace binary, keep config)
  2) Reinstall (replace binary, reinitialize)
  3) Cancel

Choice [1]: " "1")
    printf '\n'

    case "$choice" in
        1)
            UPGRADE_MODE="upgrade"
            stop_running_services
            ;;
        2)
            UPGRADE_MODE="reinstall"
            stop_running_services
            ;;
        3|*)
            log "Cancelled."
            exit 0
            ;;
    esac
}

stop_running_services() {
    if [ "$GOOS" = "linux" ]; then
        for svc in shurli-daemon shurli-relay; do
            if systemctl is-active --quiet "$svc" 2>/dev/null; then
                info "Stopping $svc..."
                run_sudo systemctl stop "$svc"
                log "Stopped."
            fi
        done
    elif [ "$GOOS" = "darwin" ]; then
        local uid
        uid="$(id -u)"
        if launchctl list 2>/dev/null | grep -q com.shurli.daemon; then
            info "Stopping com.shurli.daemon..."
            launchctl bootout "gui/${uid}/com.shurli.daemon" 2>/dev/null || true
            log "Stopped."
        fi
    fi
}

# --- Checksum verification ---

verify_checksum() {
    local file_path="$1" file_name="$2" checksums_url="$3"
    local checksums_file expected_sum actual_sum

    checksums_file="$(mktemp)"
    if ! download "$checksums_url" "$checksums_file"; then
        rm -f "$checksums_file"
        warn "Could not download checksums. Skipping verification."
        return 0
    fi

    expected_sum="$(grep "$file_name" "$checksums_file" | awk '{print $1}')"
    rm -f "$checksums_file"

    if [ -z "$expected_sum" ]; then
        warn "No checksum found for $file_name. Skipping verification."
        return 0
    fi

    if has_cmd sha256sum; then
        actual_sum="$(sha256sum "$file_path" | awk '{print $1}')"
    elif has_cmd shasum; then
        actual_sum="$(shasum -a 256 "$file_path" | awk '{print $1}')"
    else
        warn "No sha256sum or shasum found. Skipping verification."
        return 0
    fi

    if [ "$actual_sum" != "$expected_sum" ]; then
        rm -f "$file_path"
        error "Checksum mismatch!\n  Expected: $expected_sum\n  Got:      $actual_sum\n\nThe download may be corrupted. Try again or report at ${REPO_URL}/issues"
    fi
    log "Checksum verified."
}

# === METHOD 1: Download pre-built archive ===

do_download() {
    local archive_name="shurli-${VERSION}-${GOOS}-${GOARCH}.tar.gz"
    local base_url="${REPO_URL}/releases/download/${VERSION}"
    local archive_url="${base_url}/${archive_name}"
    local checksums_url="${base_url}/checksums-sha256.txt"

    info "Downloading ${archive_name}..."
    WORK_DIR="$(mktemp -d)"
    local archive_path="${WORK_DIR}/${archive_name}"

    if ! download "$archive_url" "$archive_path"; then
        rm -rf "$WORK_DIR"
        error "Download failed.\n  URL: ${archive_url}\n\nCheck that version '${VERSION}' exists at ${REPO_URL}/releases"
    fi

    if [ "$NO_VERIFY" != "yes" ]; then
        info "Verifying checksum..."
        verify_checksum "$archive_path" "$archive_name" "$checksums_url"
    fi

    info "Extracting..."
    tar xzf "$archive_path" -C "$WORK_DIR"
    rm -f "$archive_path"

    # Find the extracted directory
    ARCHIVE_DIR="$(find "$WORK_DIR" -mindepth 1 -maxdepth 1 -type d | head -1)"
    if [ -z "$ARCHIVE_DIR" ] || [ ! -f "${ARCHIVE_DIR}/${BINARY_NAME}${SUFFIX}" ]; then
        rm -rf "$WORK_DIR"
        error "Archive does not contain expected binary."
    fi
}

# === METHOD 2: Build from source (isolated environment) ===

do_build() {
    info "Build from source (isolated environment)"
    log "This will NOT touch your system Go installation."

    # Ensure git is available
    has_cmd git || error "git is required for building from source."

    WORK_DIR="$(mktemp -d)"
    BUILD_DIR="${WORK_DIR}/build"
    mkdir -p "$BUILD_DIR"

    local go_version
    # We need the go.mod to know the Go version. Fetch it from the repo tag.
    info "Fetching Go version requirement..."
    local gomod_url="${REPO_URL}/raw/${VERSION}/go.mod"
    local gomod_file="${BUILD_DIR}/go.mod.tmp"
    if ! download "$gomod_url" "$gomod_file"; then
        error "Failed to fetch go.mod for ${VERSION}. Check the version exists."
    fi
    go_version="$(grep '^go ' "$gomod_file" | awk '{print $2}')"
    rm -f "$gomod_file"
    [ -z "$go_version" ] && error "Could not determine Go version from go.mod"
    log "Required Go: ${go_version}"

    # Install build deps on Linux
    if [ "$GOOS" = "linux" ]; then
        info "Installing build dependencies..."
        if has_cmd apt-get; then
            run_sudo apt-get update -qq >/dev/null 2>&1
            run_sudo apt-get install -y -qq build-essential libavahi-compat-libdnssd-dev git >/dev/null 2>&1
            log "Installed: build-essential, libavahi-compat-libdnssd-dev, git"
        elif has_cmd yum; then
            run_sudo yum install -y -q gcc make avahi-compat-libdns_sd-devel git >/dev/null 2>&1
            log "Installed: gcc, make, avahi-compat-libdns_sd-devel, git"
        elif has_cmd apk; then
            run_sudo apk add --quiet build-base avahi-dev git >/dev/null 2>&1
            log "Installed: build-base, avahi-dev, git"
        else
            warn "Unknown package manager. Ensure build-essential and libavahi are installed."
        fi
    fi

    # Check available memory and add swap if needed (low-memory VPS)
    if [ "$GOOS" = "linux" ]; then
        local mem_mb
        mem_mb="$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo 2>/dev/null)" || mem_mb="9999"
        local swap_mb
        swap_mb="$(awk '/SwapTotal/ {printf "%d", $2/1024}' /proc/meminfo 2>/dev/null)" || swap_mb="9999"
        local total_mb=$((mem_mb + swap_mb))
        TEMP_SWAP=""

        if [ "$total_mb" -lt 1500 ]; then
            info "Low memory detected (${mem_mb}MB RAM + ${swap_mb}MB swap)"
            local add_swap
            add_swap=$(prompt "Add 1GB temporary swap for compilation? [Y/n]: " "Y")
            if [ "$add_swap" != "n" ] && [ "$add_swap" != "N" ]; then
                TEMP_SWAP="${BUILD_DIR}/swapfile"
                run_sudo dd if=/dev/zero of="$TEMP_SWAP" bs=1M count=1024 status=none 2>/dev/null
                run_sudo chmod 600 "$TEMP_SWAP"
                run_sudo mkswap "$TEMP_SWAP" >/dev/null 2>&1
                run_sudo swapon "$TEMP_SWAP"
                log "Temporary 1GB swap added."
            fi
        fi
    fi

    # Download Go to isolated dir
    info "Downloading Go ${go_version}..."
    local go_tarball="go${go_version}.${GOOS}-${GOARCH}.tar.gz"
    local go_url="https://go.dev/dl/${go_tarball}"
    if ! download "$go_url" "${BUILD_DIR}/${go_tarball}"; then
        error "Failed to download Go ${go_version}."
    fi
    tar xzf "${BUILD_DIR}/${go_tarball}" -C "$BUILD_DIR"
    rm -f "${BUILD_DIR}/${go_tarball}"
    local GO_BIN="${BUILD_DIR}/go/bin/go"
    log "Go ${go_version} ready (isolated)."

    # Clone repo
    info "Cloning Shurli ${VERSION}..."
    local repo_dir="${BUILD_DIR}/shurli"
    if ! git clone --depth 1 --branch "$VERSION" "${REPO_URL}.git" "$repo_dir" 2>/dev/null; then
        error "Failed to clone repository at tag ${VERSION}."
    fi
    log "Source ready."

    # Build
    info "Building Shurli ${VERSION}..."
    log "This may take several minutes on first build."
    local commit
    commit="$(git -C "$repo_dir" rev-parse --short HEAD 2>/dev/null || echo "unknown")"
    local build_date
    build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    local ldflags="-X main.version=${VERSION} -X main.commit=${commit} -X main.buildDate=${build_date} -s -w"

    GOROOT="${BUILD_DIR}/go" \
    GOPATH="${BUILD_DIR}/gopath" \
    GOCACHE="${BUILD_DIR}/gocache" \
    CGO_ENABLED=1 \
    "$GO_BIN" build -ldflags "$ldflags" -trimpath \
        -o "${BUILD_DIR}/shurli${SUFFIX}" \
        "${repo_dir}/cmd/shurli" || error "Build failed. Check errors above."

    log "Build complete."

    # Set up archive layout (same structure as pre-built download)
    ARCHIVE_DIR="${WORK_DIR}/shurli-${VERSION}-${GOOS}-${GOARCH}"
    mkdir -p "${ARCHIVE_DIR}/deploy" "${ARCHIVE_DIR}/tools"
    cp "${BUILD_DIR}/shurli${SUFFIX}" "${ARCHIVE_DIR}/"
    cp "${repo_dir}/deploy/shurli-daemon.service" "${repo_dir}/deploy/shurli-relay.service" "${ARCHIVE_DIR}/deploy/"
    cp "${repo_dir}/deploy/com.shurli.daemon.plist" "${ARCHIVE_DIR}/deploy/" 2>/dev/null || true
    cp "${repo_dir}/tools/relay-setup.sh" "${ARCHIVE_DIR}/tools/"
    chmod +x "${ARCHIVE_DIR}/tools/relay-setup.sh"

    # Clean up build environment
    info "Cleaning build environment..."
    rm -rf "$BUILD_DIR"

    # Remove temporary swap if we added it
    if [ -n "${TEMP_SWAP:-}" ] && [ -f "${TEMP_SWAP}" ]; then
        run_sudo swapoff "$TEMP_SWAP" 2>/dev/null || true
        run_sudo rm -f "$TEMP_SWAP"
        log "Temporary swap removed."
    fi

    log "Build artifacts cleaned. Only the binary and support files remain."
}

# === Install binary ===

install_binary() {
    local src="${ARCHIVE_DIR}/${BINARY_NAME}${SUFFIX}"
    local use_sudo="no"

    if [ -n "$OPT_DIR" ]; then
        INSTALL_TO="$OPT_DIR"
    elif [ -w "$INSTALL_DIR" ] || [ "$(id -u)" -eq 0 ]; then
        INSTALL_TO="$INSTALL_DIR"
    elif has_cmd sudo; then
        INSTALL_TO="$INSTALL_DIR"
        use_sudo="yes"
    else
        INSTALL_TO="$USER_INSTALL_DIR"
    fi

    info "Installing binary to ${INSTALL_TO}..."

    if [ "$use_sudo" = "yes" ] || { [ "$INSTALL_TO" = "$INSTALL_DIR" ] && [ "$(id -u)" -ne 0 ]; }; then
        sudo install -m 755 "$src" "${INSTALL_TO}/${BINARY_NAME}${SUFFIX}"
    else
        mkdir -p "$INSTALL_TO"
        install -m 755 "$src" "${INSTALL_TO}/${BINARY_NAME}${SUFFIX}"
    fi

    # macOS: codesign for stable Local Network Privacy identity
    if [ "$GOOS" = "darwin" ]; then
        codesign -s - -f "${INSTALL_TO}/${BINARY_NAME}" 2>/dev/null || true
    fi

    log "Installed: ${INSTALL_TO}/${BINARY_NAME}${SUFFIX}"

    # Verify it runs
    if [ -x "${INSTALL_TO}/${BINARY_NAME}${SUFFIX}" ]; then
        local ver_out
        ver_out="$("${INSTALL_TO}/${BINARY_NAME}${SUFFIX}" --version 2>/dev/null | head -1)" || ver_out=""
        [ -n "$ver_out" ] && log "$ver_out"
    fi

    # Warn if not in PATH
    case ":${PATH}:" in
        *":${INSTALL_TO}:"*) ;;
        *)
            printf '\n'
            warn "${INSTALL_TO} is not in your PATH."
            log "Add it:  export PATH=\"\$PATH:${INSTALL_TO}\""
            ;;
    esac
}

# === Role: Peer node setup ===

setup_peer() {
    info "Setting up peer node..."

    # Install runtime deps on Linux
    if [ "$GOOS" = "linux" ]; then
        local pkgs=""
        dpkg -s libavahi-compat-libdnssd1 >/dev/null 2>&1 || pkgs="libavahi-compat-libdnssd1"
        has_cmd qrencode || pkgs="$pkgs qrencode"
        if [ -n "$pkgs" ]; then
            log "Installing runtime dependencies:$pkgs"
            run_sudo apt-get update -qq >/dev/null 2>&1
            run_sudo apt-get install -y -qq $pkgs >/dev/null 2>&1
        fi
    fi

    # Install systemd service (Linux)
    if [ "$GOOS" = "linux" ] && has_cmd systemctl; then
        local service_src="${ARCHIVE_DIR}/deploy/shurli-daemon.service"
        local service_dest="/etc/systemd/system/shurli-daemon.service"

        if [ -f "$service_src" ]; then
            local svc_user
            if [ "$(id -u)" -eq 0 ]; then
                svc_user="${SUDO_USER:-root}"
            else
                svc_user="$(whoami)"
            fi

            info "Installing systemd service for user '${svc_user}'..."
            run_sudo cp "$service_src" "$service_dest"
            run_sudo sed -i "s|^User=.*|User=${svc_user}|" "$service_dest"
            run_sudo sed -i "s|^Group=.*|Group=${svc_user}|" "$service_dest"
            run_sudo sed -i "s|^ReadWritePaths=.*|ReadWritePaths=/home/${svc_user}/.config/shurli /home/${svc_user}/Downloads/shurli /run/user|" "$service_dest"
            run_sudo systemctl daemon-reload
            log "Service installed: shurli-daemon.service"
        fi
    fi

    # Install launchd plist (macOS)
    if [ "$GOOS" = "darwin" ]; then
        local plist_src="${ARCHIVE_DIR}/deploy/com.shurli.daemon.plist"
        local plist_dest="${HOME}/Library/LaunchAgents/com.shurli.daemon.plist"

        if [ -f "$plist_src" ]; then
            info "Installing launchd service..."
            mkdir -p "$(dirname "$plist_dest")"
            cp "$plist_src" "$plist_dest"
            log "Plist installed: ${plist_dest}"
        fi
    fi

    # Run shurli init or join (interactive)
    local bin="${INSTALL_TO}/${BINARY_NAME}${SUFFIX}"

    if [ "${UPGRADE_MODE:-}" = "upgrade" ]; then
        info "Upgrade complete. Restarting service..."
        restart_peer_service
        return
    fi

    # Check if config already exists
    local config_exists="no"
    for cfg_dir in /etc/shurli "${HOME}/.config/shurli"; do
        if [ -f "${cfg_dir}/config.yaml" ]; then
            config_exists="yes"
            break
        fi
    done

    if [ "$config_exists" = "yes" ] && [ "${UPGRADE_MODE:-}" != "reinstall" ]; then
        info "Existing config found at ${cfg_dir}/config.yaml"
        log "Skipping initialization. Starting service..."
        restart_peer_service
        return
    fi

    if [ "${UPGRADE_MODE:-}" = "reinstall" ] && [ "$config_exists" = "yes" ]; then
        info "Reinitializing..."
        local backup_choice
        backup_choice=$(prompt "Back up existing config before reinitializing? [Y/n]: " "Y")
        if [ "$backup_choice" != "n" ] && [ "$backup_choice" != "N" ]; then
            local backup_dir="${cfg_dir}.backup.$(date +%Y%m%d%H%M%S)"
            run_sudo cp -a "$cfg_dir" "$backup_dir" 2>/dev/null || cp -a "$cfg_dir" "$backup_dir"
            log "Backed up to: ${backup_dir}"
        fi
        run_sudo rm -f "${cfg_dir}/config.yaml" 2>/dev/null || rm -f "${cfg_dir}/config.yaml"
    fi

    printf '\n'
    bold "Running shurli init..."
    printf '\n'
    "$bin" init </dev/tty

    # Start service
    restart_peer_service
}

restart_peer_service() {
    if [ "$GOOS" = "linux" ] && has_cmd systemctl; then
        if systemctl is-enabled --quiet shurli-daemon 2>/dev/null; then
            run_sudo systemctl restart shurli-daemon
            log "Service restarted."
        else
            run_sudo systemctl enable shurli-daemon
            run_sudo systemctl start shurli-daemon
            log "Service enabled and started."
        fi
        log "Logs: journalctl -u shurli-daemon -f"
    elif [ "$GOOS" = "darwin" ]; then
        local uid
        uid="$(id -u)"
        local plist_dest="${HOME}/Library/LaunchAgents/com.shurli.daemon.plist"
        if [ -f "$plist_dest" ]; then
            launchctl bootout "gui/${uid}/com.shurli.daemon" 2>/dev/null || true
            launchctl bootstrap "gui/${uid}" "$plist_dest"
            log "Service started."
            log "Logs: /tmp/shurli-daemon.log"
        fi
    fi
}

# === Role: Relay server setup ===

setup_relay() {
    info "Setting up relay server..."

    if [ "$GOOS" != "linux" ]; then
        warn "Relay setup is designed for Linux servers."
        log "On other platforms, run 'shurli relay serve' manually."
        return
    fi

    local relay_setup="${ARCHIVE_DIR}/tools/relay-setup.sh"
    if [ ! -f "$relay_setup" ]; then
        error "relay-setup.sh not found in archive. Cannot continue."
    fi

    # Run relay-setup.sh in prebuilt mode
    chmod +x "$relay_setup"
    bash "$relay_setup" --prebuilt --deploy-dir "${ARCHIVE_DIR}/deploy" </dev/tty
}

# === Role: Binary only ===

setup_binary_only() {
    if [ "${UPGRADE_MODE:-}" = "upgrade" ]; then
        info "Binary upgraded. Restarting services..."
        # Restart whatever was running
        if [ "$GOOS" = "linux" ] && has_cmd systemctl; then
            for svc in shurli-daemon shurli-relay; do
                if systemctl is-enabled --quiet "$svc" 2>/dev/null; then
                    run_sudo systemctl restart "$svc"
                    log "Restarted $svc."
                fi
            done
        elif [ "$GOOS" = "darwin" ]; then
            restart_peer_service
        fi
    else
        printf '\n'
        log "Next steps:"
        log "  shurli init                  Set up as a peer node"
        log "  shurli join --relay          Join via relay server"
        log "  shurli relay setup           Set up as a relay server"
        printf '\n'
        log "Docs: https://shurli.io/docs/quick-start/"
    fi
}

# === Cleanup ===

cleanup() {
    if [ -n "${WORK_DIR:-}" ] && [ -d "${WORK_DIR:-}" ]; then
        rm -rf "$WORK_DIR"
    fi
}

# === Main ===

main() {
    # Parse arguments
    VERSION=""
    METHOD=""
    ROLE=""
    OPT_DIR=""
    NO_VERIFY=""
    UPGRADE_MODE=""

    while [ $# -gt 0 ]; do
        case "$1" in
            --version)   VERSION="$2"; shift 2 ;;
            --method)    METHOD="$2"; shift 2 ;;
            --role)      ROLE="$2"; shift 2 ;;
            --dir)       OPT_DIR="$2"; shift 2 ;;
            --no-verify) NO_VERIFY="yes"; shift ;;
            --help|-h)
                printf 'Shurli Installer\n\n'
                printf 'Usage: install.sh [--version VERSION] [--method download|build]\n'
                printf '                  [--role peer|relay|binary] [--dir DIR] [--no-verify]\n'
                exit 0
                ;;
            *) error "Unknown option: $1" ;;
        esac
    done

    trap cleanup EXIT

    # 1. Platform detection
    detect_platform
    info "Shurli Installer"
    log "Platform: ${GOOS}/${GOARCH}"

    # 2. Resolve version
    if [ -z "$VERSION" ]; then
        info "Fetching latest version..."
        fetch_latest_version
    fi
    log "Version: ${VERSION}"

    # 3. Check for existing install
    check_existing_install
    if [ -n "$EXISTING_PATH" ]; then
        handle_existing_install
    fi

    # 4. Choose install method
    if [ -z "$METHOD" ]; then
        printf '\n'
        bold "How would you like to install Shurli?"
        log "1) Download pre-built binary (fastest, recommended)"
        log "2) Build from source (isolated build environment)"
        printf '\n'
        METHOD=$(prompt "Choice [1]: " "1")
        case "$METHOD" in
            1|download) METHOD="download" ;;
            2|build)    METHOD="build" ;;
            *)          METHOD="download" ;;
        esac
    fi

    # 5. Get the binary
    case "$METHOD" in
        download) do_download ;;
        build)    do_build ;;
        *)        error "Unknown method: $METHOD" ;;
    esac

    # 6. Install binary
    install_binary

    # 7. Choose role
    if [ -z "$ROLE" ]; then
        printf '\n'
        bold "What would you like to set up?"
        log "1) Peer node (home server, desktop, Raspberry Pi)"
        log "2) Relay server (VPS, cloud server)"
        log "3) Binary only (skip setup)"
        printf '\n'
        ROLE=$(prompt "Choice [1]: " "1")
        case "$ROLE" in
            1|peer)   ROLE="peer" ;;
            2|relay)  ROLE="relay" ;;
            3|binary) ROLE="binary" ;;
            *)        ROLE="peer" ;;
        esac
    fi

    # 8. Run role setup
    case "$ROLE" in
        peer)   setup_peer ;;
        relay)  setup_relay ;;
        binary) setup_binary_only ;;
    esac

    info "Done!"
    printf '\n'
}

main "$@"
