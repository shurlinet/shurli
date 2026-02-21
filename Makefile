# peer-up Makefile
# Build, test, install, and manage the peerup daemon.

BINARY     := peerup
INSTALL_DIR := /usr/local/bin
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE) -s -w
OS         := $(shell uname -s)

SYSTEMD_SERVICE := deploy/peerup-daemon.service
SYSTEMD_DEST    := /etc/systemd/system/peerup-daemon.service
LAUNCHD_PLIST   := deploy/com.peerup.daemon.plist
LAUNCHD_DEST    := $(HOME)/Library/LaunchAgents/com.peerup.daemon.plist
LAUNCHD_LABEL   := com.peerup.daemon

.PHONY: build test clean install install-service uninstall-service uninstall restart-service website check push help

## Build the peerup binary with version embedding.
build:
	go build -ldflags "$(LDFLAGS)" -trimpath -o $(BINARY) ./cmd/peerup

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
install-service:
ifeq ($(OS),Linux)
	@echo "Installing systemd service..."
	@echo "This requires elevated permissions."
	@if ! id -u peerup >/dev/null 2>&1; then \
		echo "Creating system user 'peerup'..."; \
		sudo useradd --system --shell /usr/sbin/nologin --create-home peerup; \
		sudo mkdir -p /home/peerup/.config/peerup; \
		sudo chown peerup:peerup /home/peerup/.config/peerup; \
		echo "User 'peerup' created."; \
	fi
	sudo cp $(SYSTEMD_SERVICE) $(SYSTEMD_DEST)
	sudo systemctl daemon-reload
	sudo systemctl enable peerup-daemon
	@echo ""
	@echo "Service installed and enabled."
	@echo "Start with: sudo systemctl start peerup-daemon"
	@echo "Logs:       journalctl -u peerup-daemon -f"
else ifeq ($(OS),Darwin)
	@echo "Installing launchd service..."
	@mkdir -p $(dir $(LAUNCHD_DEST))
	cp $(LAUNCHD_PLIST) $(LAUNCHD_DEST)
	launchctl load $(LAUNCHD_DEST)
	@echo ""
	@echo "Service installed and loaded."
	@echo "Logs: /tmp/peerup-daemon.log"
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
	-sudo systemctl stop peerup-daemon 2>/dev/null
	-sudo systemctl disable peerup-daemon 2>/dev/null
	-sudo rm -f $(SYSTEMD_DEST)
	-sudo systemctl daemon-reload
	@echo "Service removed."
else ifeq ($(OS),Darwin)
	@echo "Removing launchd service..."
	-launchctl unload $(LAUNCHD_DEST) 2>/dev/null
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
	sudo systemctl restart peerup-daemon
	@echo "Service restarted."
	@echo "Status: sudo systemctl status peerup-daemon"
else ifeq ($(OS),Darwin)
	@echo "Restarting launchd service..."
	launchctl kickstart -k gui/$$(id -u)/$(LAUNCHD_LABEL)
	@echo "Service restarted."
else
	@echo "Unsupported OS: $(OS)"
endif

## Start the Hugo development server for the website.
website:
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
	@echo "peer-up Makefile targets:"
	@echo ""
	@echo "  build             Build the peerup binary"
	@echo "  test              Run all tests with race detection"
	@echo "  clean             Remove build artifacts"
	@echo "  install           Build, install binary, and set up service"
	@echo "  install-service   Install and enable system service"
	@echo "  uninstall-service Stop and remove system service"
	@echo "  uninstall         Remove service and binary"
	@echo "  restart-service   Restart the system service"
	@echo "  website           Start Hugo development server"
	@echo "  check             Run local checks from .checks file"
	@echo "  push              Run checks, then git push"
	@echo "  help              Show this help"
