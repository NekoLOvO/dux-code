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

.PHONY: all build dev run hot check check-static test vet fmt i18n i18n-status qmllint qmllint-full release install install-bin install-assets uninstall update-common help

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
	@$(MAKE) --no-print-directory check-static
	@$(MAKE) --no-print-directory qmllint

# Everything in `check` that needs no Qt. CI runs this and stops there: qmllint is version-shaped
# (see the qmllint target), and no Qt it can install answers like the one this tree is developed
# against — the official 6.12 Linux binaries link ICU 73, and the runner's Ubuntu has ICU 74, so
# they do not start at all.
check-static:
	@./tools/check-upstream.sh
	@./tools/check-theme-tokens.py
	@./tools/check-windows.py
	@./tools/check-english.py
	@./tools/check-signal-arity.py
	@./tools/check-desktop.py
	@./tools/test-version.sh

# qmllint needs a qmldir index for the Quickshell `qs.*` imports, which the tree does not carry (see
# tools/qmllint-farm.py). The farm is generated into the cache and passed with -I.
#
# tools/qmllint-report.py runs it and parses --json, because qmllint's text output omits most of the
# diagnostics it counts. A warning fails the run: MAX_WARNINGS=-1 reports without failing.
QML_SOURCES=$(shell find . -name '*.qml' -not -path './dux-qml-common/*' -not -path './dist/*' -not -path './core/internal/uibundle/dist/*')
QMLLINT_FARM=$(or $(XDG_CACHE_HOME),$(HOME)/.cache)/dcode-qmllint
# Qt 6's linter (installed as /usr/local/bin/qmllint); QMLLINT= overrides. `qmllint` refuses any
# other major version, since Qt 5 shipped a different tool under the same name.
QMLLINT ?= qmllint
MAX_WARNINGS ?= 0

qmllint:
	@$(QMLLINT) --version 2>/dev/null | grep -q '^qmllint 6' || \
		{ echo "$(QMLLINT) is not Qt 6's qmllint; set QMLLINT=/usr/lib/qt6/bin/qmllint"; exit 1; }
	@python3 tools/qmllint-farm.py "$(QMLLINT_FARM)"
	@QMLLINT="$(QMLLINT)" python3 tools/qmllint-report.py -I "$(QMLLINT_FARM)" \
		--max-warnings=$(MAX_WARNINGS) $(QML_SOURCES)

# Every diagnostic at the level Qt gives it, settings ignored.
qmllint-full:
	@python3 tools/qmllint-farm.py "$(QMLLINT_FARM)"
	@QMLLINT="$(QMLLINT)" python3 tools/qmllint-report.py -I "$(QMLLINT_FARM)" \
		--ignore-settings --all --max-warnings=-1 $(QML_SOURCES)

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
	@echo "  check          - upstream contract, Theme tokens, window parents, English source, version"
	@echo "  check-static   - the same without qmllint (no Qt needed; this is what CI runs)"
	@echo "  qmllint        - QML lint (MAX_WARNINGS=0 to make it a gate)"
	@echo "  qmllint-full   - the same, ignoring .qmllint.ini"
	@echo "  test / vet / fmt"
	@echo "  i18n           - re-extract I18n.tr() strings -> translations/template.json"
	@echo "  i18n-status    - how many strings each locale is missing"
	@echo ""
	@echo "Release:"
	@echo "  release        - build dist/dcode-linux-amd64.tar.gz"
	@echo "  install        - install to $(PREFIX) (PREFIX=/usr make install)"
	@echo "  uninstall      - remove"
	@echo "  update-common  - move the component-library submodule to upstream latest"
