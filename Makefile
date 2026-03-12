# Shurli Makefile
# Build, test, install, and manage the shurli daemon and relay server.

BINARY     := shurli
INSTALL_DIR := /usr/local/bin
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE) -s -w
OS         := $(shell uname -s)

SYSTEMD_SERVICE := deploy/shurli-daemon.service
SYSTEMD_DEST    := /etc/systemd/system/shurli-daemon.service
LAUNCHD_PLIST   := deploy/com.shurli.daemon.plist
LAUNCHD_DEST    := $(HOME)/Library/LaunchAgents/com.shurli.daemon.plist
LAUNCHD_LABEL   := com.shurli.daemon

# Relay server variables (SERVICE_USER: SUDO_USER if under sudo, else whoami)
SERVICE_USER       ?= $(or $(SUDO_USER),$(shell whoami))
RELAY_DATA_DIR     := /etc/shurli/relay
RELAY_SERVICE      := deploy/shurli-relay.service
RELAY_SERVICE_DEST := /etc/systemd/system/shurli-relay.service

.PHONY: build test clean install install-service uninstall-service uninstall restart-service \
        install-relay install-relay-service uninstall-relay-service uninstall-relay \
        sync-docs website check push help

## Build the shurli binary with version embedding.
build:
	go build -ldflags "$(LDFLAGS)" -trimpath -o $(BINARY) ./cmd/shurli

## Run all tests with race detection.
test:
	go test -race -count=1 ./...

## Remove build artifacts.
clean:
	rm -f $(BINARY)

## Build, install the binary, and set up the system service.
install: build
	@echo "Installing $(BINARY) to $(INSTALL_DIR)/$(BINARY)"
	@echo "This requires elevated permissions."
	sudo install -m 755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Binary installed: $(INSTALL_DIR)/$(BINARY)"
	@$(MAKE) install-service

## Install and enable the system service (auto-detects OS).
## On Linux, prompts for the user to run the daemon as.
## Override with: make install-service SERVICE_USER=myuser
## Run 'shurli init' as that user first to create the config.
install-service:
ifeq ($(OS),Linux)
ifndef SHURLI_SERVICE_USER_CONFIRMED
	@echo ""
	@echo "The daemon service needs a user to run as."
	@echo "This should be the user who ran 'shurli init'."
	@echo ""
	@echo "  Detected user: $(SERVICE_USER)"
	@echo ""
	@read -p "Run daemon as '$(SERVICE_USER)'? [Y/n/username]: " answer; \
	if [ "$$answer" = "n" ] || [ "$$answer" = "N" ]; then \
		echo "Aborted. Use: make install-service SERVICE_USER=<username>"; \
		exit 1; \
	elif [ -n "$$answer" ] && [ "$$answer" != "y" ] && [ "$$answer" != "Y" ]; then \
		if ! id -u "$$answer" >/dev/null 2>&1; then \
			echo "Error: user '$$answer' does not exist."; \
			exit 1; \
		fi; \
		$(MAKE) install-service SERVICE_USER="$$answer" SHURLI_SERVICE_USER_CONFIRMED=1; \
		exit 0; \
	fi; \
	$(MAKE) install-service SHURLI_SERVICE_USER_CONFIRMED=1
else
	@echo "Installing systemd service for user '$(SERVICE_USER)'..."
	@if ! id -u "$(SERVICE_USER)" >/dev/null 2>&1; then \
		echo "Error: user '$(SERVICE_USER)' does not exist."; \
		echo "Create the user first, or choose a different one."; \
		exit 1; \
	fi
	sudo cp $(SYSTEMD_SERVICE) $(SYSTEMD_DEST)
	sudo sed -i 's|^User=.*|User=$(SERVICE_USER)|' $(SYSTEMD_DEST)
	sudo sed -i 's|^Group=.*|Group=$(SERVICE_USER)|' $(SYSTEMD_DEST)
	sudo sed -i 's|^ReadWritePaths=.*|ReadWritePaths=/home/$(SERVICE_USER)/.config/shurli /home/$(SERVICE_USER)/Downloads/shurli /run/user|' $(SYSTEMD_DEST)
	sudo systemctl daemon-reload
	sudo systemctl enable shurli-daemon
	@echo ""
	@echo "Service installed and enabled for user '$(SERVICE_USER)'."
	@echo "Make sure 'shurli init' has been run as '$(SERVICE_USER)' first."
	@echo "Start with: sudo systemctl start shurli-daemon"
	@echo "Logs:       journalctl -u shurli-daemon -f"
endif
else ifeq ($(OS),Darwin)
	@echo "Installing launchd service..."
	@mkdir -p $(dir $(LAUNCHD_DEST))
	cp $(LAUNCHD_PLIST) $(LAUNCHD_DEST)
	-launchctl bootout gui/$$(id -u)/$(LAUNCHD_LABEL) 2>/dev/null
	launchctl bootstrap gui/$$(id -u) $(LAUNCHD_DEST)
	@echo ""
	@echo "Service installed and loaded."
	@echo "Logs: /tmp/shurli-daemon.log"
else
	@echo "Unsupported OS for service install: $(OS)"
	@echo "Supported: Linux (systemd), macOS (launchd)"
	@exit 1
endif

## Stop and remove the system service.
uninstall-service:
ifeq ($(OS),Linux)
	@echo "Removing systemd service..."
	@echo "This requires elevated permissions."
	-sudo systemctl stop shurli-daemon 2>/dev/null
	-sudo systemctl disable shurli-daemon 2>/dev/null
	-sudo rm -f $(SYSTEMD_DEST)
	-sudo systemctl daemon-reload
	@echo "Service removed."
else ifeq ($(OS),Darwin)
	@echo "Removing launchd service..."
	-launchctl bootout gui/$$(id -u)/$(LAUNCHD_LABEL) 2>/dev/null
	-rm -f $(LAUNCHD_DEST)
	@echo "Service removed."
else
	@echo "Unsupported OS: $(OS)"
endif

## Remove the service and the installed binary.
uninstall: uninstall-service
	@echo "Removing $(INSTALL_DIR)/$(BINARY)"
	@echo "This requires elevated permissions."
	sudo rm -f $(INSTALL_DIR)/$(BINARY)
	@echo "Uninstall complete."

## Restart the system service.
restart-service:
ifeq ($(OS),Linux)
	@echo "Restarting systemd service..."
	@echo "This requires elevated permissions."
	sudo systemctl restart shurli-daemon
	@echo "Service restarted."
	@echo "Status: sudo systemctl status shurli-daemon"
else ifeq ($(OS),Darwin)
	@echo "Restarting launchd service..."
	launchctl kickstart -k gui/$$(id -u)/$(LAUNCHD_LABEL)
	@echo "Service restarted."
else
	@echo "Unsupported OS: $(OS)"
endif

## Build, install the binary, create relay data dir, and set up relay service (Linux only).
install-relay: build
ifeq ($(OS),Linux)
	@echo "Installing relay server to system paths..."
	@echo "This requires elevated permissions."
	sudo install -m 755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Binary installed: $(INSTALL_DIR)/$(BINARY)"
	@if ! id -u $(SERVICE_USER) >/dev/null 2>&1; then \
		echo "Error: user '$(SERVICE_USER)' does not exist."; \
		echo "Create it first, or use: make install-relay SERVICE_USER=<existing-user>"; \
		exit 1; \
	fi
	sudo mkdir -p $(RELAY_DATA_DIR)
	sudo chown $(SERVICE_USER):$(SERVICE_USER) $(RELAY_DATA_DIR)
	sudo chmod 700 $(RELAY_DATA_DIR)
	@echo "Data directory: $(RELAY_DATA_DIR)"
	@if [ -f $(RELAY_DATA_DIR)/relay-server.yaml ]; then \
		echo "Config already exists, skipping relay setup."; \
	else \
		sudo -u $(SERVICE_USER) $(INSTALL_DIR)/$(BINARY) relay setup --dir $(RELAY_DATA_DIR) --non-interactive; \
		sudo chmod 600 $(RELAY_DATA_DIR)/relay-server.yaml $(RELAY_DATA_DIR)/relay_authorized_keys 2>/dev/null || true; \
	fi
	@$(MAKE) install-relay-service
else
	@echo "Relay install is Linux-only (requires systemd)."
	@echo "On other platforms, run 'shurli relay serve' manually."
	@exit 1
endif

## Install the relay systemd service file (does not enable or start).
## The setup script handles identity creation first, then enables the service.
install-relay-service:
ifeq ($(OS),Linux)
	@echo "Installing relay systemd service file..."
	sudo cp $(RELAY_SERVICE) $(RELAY_SERVICE_DEST)
	sudo sed -i 's/^User=.*/User=$(SERVICE_USER)/' $(RELAY_SERVICE_DEST)
	sudo sed -i 's/^Group=.*/Group=$(SERVICE_USER)/' $(RELAY_SERVICE_DEST)
	sudo systemctl daemon-reload
	@echo ""
	@echo "Relay service file installed (not yet enabled)."
	@echo "Run the identity setup, then enable with: sudo systemctl enable --now shurli-relay"
else
	@echo "Relay service install is Linux-only (requires systemd)."
	@exit 1
endif

## Stop and remove the relay systemd service.
uninstall-relay-service:
ifeq ($(OS),Linux)
	@echo "Removing relay systemd service..."
	-sudo systemctl stop shurli-relay 2>/dev/null
	-sudo systemctl disable shurli-relay 2>/dev/null
	-sudo rm -f $(RELAY_SERVICE_DEST)
	-sudo systemctl daemon-reload
	@echo "Relay service removed."
else
	@echo "Unsupported OS: $(OS)"
endif

## Remove the relay service and the installed binary (preserves config/keys).
uninstall-relay: uninstall-relay-service
	@echo "Removing $(INSTALL_DIR)/$(BINARY)"
	@echo "This requires elevated permissions."
	sudo rm -f $(INSTALL_DIR)/$(BINARY)
	@echo "Uninstall complete."
	@echo "Config and keys preserved in: $(RELAY_DATA_DIR)"
	@echo "To remove data: sudo rm -rf $(RELAY_DATA_DIR)"

## Sync docs/ to website/ with Hugo front matter and link rewriting.
sync-docs:
	go run ./tools/sync-docs

## Sync docs, then start the Hugo development server.
website: sync-docs
	cd website && hugo mod tidy && hugo server

## Run local checks from .checks file (one command per line).
check:
	@if [ ! -f .checks ]; then \
		echo "No .checks file found."; \
		echo "Create one with commands to run (one per line)."; \
		echo "Lines starting with # are ignored."; \
		exit 0; \
	fi; \
	echo "Running local checks..."; \
	failed=0; \
	while IFS= read -r cmd || [ -n "$$cmd" ]; do \
		case "$$cmd" in \
			""|\#*) continue ;; \
		esac; \
		echo "  -> $$cmd"; \
		if ! eval "$$cmd" >/dev/null 2>&1; then \
			echo "  FAILED"; \
			failed=1; \
		fi; \
	done < .checks; \
	if [ "$$failed" -ne 0 ]; then \
		echo "Some checks failed."; \
		exit 1; \
	fi; \
	echo "All checks passed."

## Run local checks, then push to remote.
push: check
	git push

## Show available targets.
help:
	@echo "Shurli Makefile targets:"
	@echo ""
	@echo "  build                  Build the shurli binary"
	@echo "  test                   Run all tests with race detection"
	@echo "  clean                  Remove build artifacts"
	@echo ""
	@echo "  Daemon (home-node / client-node):"
	@echo "  install                Build, install binary, and set up daemon service"
	@echo "  install-service        Install and enable daemon system service"
	@echo "  uninstall-service      Stop and remove daemon system service"
	@echo "  uninstall              Remove daemon service and binary"
	@echo "  restart-service        Restart the daemon system service"
	@echo ""
	@echo "  Relay server (VPS):"
	@echo "  install-relay          Build, install binary, create /etc/shurli/relay, set up relay service"
	@echo "  install-relay-service  Install and enable relay systemd service"
	@echo "  uninstall-relay-service Stop and remove relay systemd service"
	@echo "  uninstall-relay        Remove relay service and binary (preserves config/keys)"
	@echo ""
	@echo "  Dev tools:"
	@echo "  sync-docs              Sync docs/ to website/ content"
	@echo "  website                Sync docs and start Hugo dev server"
	@echo "  check                  Run local checks from .checks file"
	@echo "  push                   Run checks, then git push"
	@echo "  help                   Show this help"
