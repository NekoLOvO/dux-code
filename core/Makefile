BINARY_NAME=dcode
SOURCE_DIR=cmd/dcode
BUILD_DIR=bin
SHELL_SRC=..
EMBED_DIR=internal/uibundle/dist

GO=go

# Only vX.Y.Z tags count. Anything else (backup markers, notes-to-self) must never reach the UI:
# with a plain `git describe`, the nearest tag of any kind wins and the About page shows it verbatim.
BASE_VERSION=$(or $(shell git describe --tags --match 'v[0-9]*' --abbrev=0 2>/dev/null | sed 's/^v//'),0.0.0)
COMMIT_COUNT=$(shell git rev-list --count HEAD 2>/dev/null || echo 0)
COMMIT_HASH=$(shell git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
VERSION?=$(BASE_VERSION)+git$(COMMIT_COUNT).$(COMMIT_HASH)
BUILD_TIME?=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
COMMIT?=$(COMMIT_HASH)

BUILD_LDFLAGS=-ldflags='-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.Commit=$(COMMIT)'

# The UI packed into the binary. Order is irrelevant; tar just walks the list.
UI_DIRS=shell.qml Common Modules Services Widgets Modals DCommon assets translations

.PHONY: all sync-shell build dev test vet fmt clean version

all: build

# Flatten the UI into the embed directory (gitignored).
#
# tar -h is essential: DCommon is a symlink into the submodule and go:embed does not follow symlinks.
# Without -h the link itself is copied, the binary ships without the component library, and the window
# comes up blank — with no warning at build time.
#
# template.json and work/ are translation tooling, not needed at runtime, so they stay out of the embed.
sync-shell:
	@test -d $(SHELL_SRC)/DCommon/Common || { echo "DCommon is missing: run git submodule update --init"; exit 1; }
	@rm -rf $(EMBED_DIR)
	@mkdir -p $(EMBED_DIR)
	@tar -C $(SHELL_SRC) -chf - --exclude='translations/template.json' --exclude='translations/work' $(UI_DIRS) | tar -C $(EMBED_DIR) -xf -

build: sync-shell
	@echo "Building $(BINARY_NAME) $(VERSION)…"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -tags embedui $(BUILD_LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(SOURCE_DIR)

# Development: no embedded UI, running the copy in the repo (dcode ui -c ..).
dev:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME) ./$(SOURCE_DIR)

test:
	@$(GO) test ./...

vet:
	@$(GO) vet ./...

# The one place the version string is assembled. `make release` asks this instead of recomputing it,
# and `make build` bakes it in through the ldflags below, so the two cannot drift apart.
version:
	@echo '$(VERSION)'

fmt:
	@gofmt -l -w .

clean:
	@rm -rf $(BUILD_DIR) $(EMBED_DIR)
