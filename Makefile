# Root Makefile for Dux Code
# Same shape as dux-calendar / dux-files: make is the only entry point, Go build details live in
# core/Makefile, and this handles development, checks, packaging and installation.

BINARY_NAME=dcode
CORE_DIR=core
BUILD_DIR=$(CORE_DIR)/bin

PREFIX ?= /usr/local
DESTDIR ?=
INSTALL_DIR=$(PREFIX)/bin
DATA_DIR=$(PREFIX)/share
APPLICATIONS_DIR=$(DATA_DIR)/applications
ICON_DIR=$(DATA_DIR)/icons/hicolor/scalable/apps

UI_DIR=.
DESKTOP_ID=com.nekolovo.dcode
ASSET_INSTALL=PREFIX="$(PREFIX)" DESTDIR="$(DESTDIR)" DCODE_EXEC="$(INSTALL_DIR)/$(BINARY_NAME)" sh scripts/install-assets.sh

.PHONY: all build dev run hot check test vet fmt i18n i18n-status release install install-bin install-assets uninstall update-common help

all: build

build:
	@$(MAKE) -C $(CORE_DIR) build

dev:
	@$(MAKE) -C $(CORE_DIR) dev

# Development runs the QML in the repo (-c), not the embedded copy; the launch logic lives in dcode ui,
# so `make run` and clicking the desktop icon after installing go through the same code.
run: dev
	@./tools/check-upstream.sh || echo "(upstream contract check failed: see UPSTREAM.md)"
	@$(BUILD_DIR)/$(BINARY_NAME) ui -c $(CURDIR)

# The same run, but always a fresh Quickshell: without it a second launch raises the old window,
# and the QML you just edited is not what you are looking at.
hot: export DUXCODE_ENABLE_HOTRELOAD = 1
hot: run

check:
	@./tools/check-upstream.sh
	@./tools/check-windows.py
	@./tools/check-english.py

test:
	@$(MAKE) -C $(CORE_DIR) test

vet:
	@$(MAKE) -C $(CORE_DIR) vet

fmt:
	@$(MAKE) -C $(CORE_DIR) fmt

i18n:
	@python3 translations/extract_translations.py

i18n-status:
	@python3 translations/extract_translations.py status

# An archive of one binary + assets/ + scripts/ (dist/, gitignored)
release:
	@./scripts/build-release.sh

install-bin: build
	@echo "Installing $(BINARY_NAME) to $(DESTDIR)$(INSTALL_DIR)…"
	@install -D -m 755 $(BUILD_DIR)/$(BINARY_NAME) $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)

install-assets:
	@$(ASSET_INSTALL)

install: install-bin install-assets
	@echo ""
	@echo "Installed. Start it from the application menu or with '$(BINARY_NAME) ui'."

uninstall:
	@rm -f $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)
	@rm -f $(DESTDIR)$(APPLICATIONS_DIR)/$(DESKTOP_ID).desktop
	@rm -f $(DESTDIR)$(ICON_DIR)/$(DESKTOP_ID).svg
	@test -n "$(DESTDIR)" || update-desktop-database -q $(APPLICATIONS_DIR) 2>/dev/null || true
	@test -n "$(DESTDIR)" || gtk-update-icon-cache -q $(DATA_DIR)/icons/hicolor 2>/dev/null || true

# The component library is a submodule: updating it is a real change, so verify before committing.
update-common:
	git submodule update --remote dux-qml-common
	@./tools/check-upstream.sh
	@echo "Verify, then commit: git add dux-qml-common && git commit"

help:
	@echo "Development:"
	@echo "  build          - build (UI embedded, for installs)"
	@echo "  dev            - quick build (UI not embedded)"
	@echo "  run            - build and run the QML in the repo"
	@echo "  hot            - the same, but a fresh shell each time (QML edits apply)"
	@echo "  check          - upstream contract, Theme tokens, window parents, English source"
	@echo "  test / vet / fmt"
	@echo "  i18n           - re-extract I18n.tr() strings -> translations/template.json"
	@echo "  i18n-status    - how many strings each locale is missing"
	@echo ""
	@echo "Release:"
	@echo "  release        - build dist/dcode-linux-amd64.tar.gz"
	@echo "  install        - install to $(PREFIX) (PREFIX=/usr make install)"
	@echo "  uninstall      - remove"
	@echo "  update-common  - move the component-library submodule to upstream latest"
