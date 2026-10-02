NAME=tinycode
BINDIR=bin
VERSION=$(shell git --no-pager describe --tags 2>/dev/null || echo "dev")
COMMIT_SHA=$(shell git --no-pager rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME=$(shell date -u)
GOBUILD=CGO_ENABLED=0 go build -trimpath -ldflags '-X "main.Version=$(VERSION)" \
		-X "main.CommitSHA=$(COMMIT_SHA)" \
		-X "main.BuildTime=$(BUILDTIME)" \
		-w -s -buildid='

PLATFORM_LIST = \
	linux-amd64 \
	linux-arm64 \
	darwin-arm64

.PHONY: default build run test test-race test-repeat test-lsp test-browser test-tui-visual install-gopls install-tsls test-tuiprobe test-tuiprobe-race lint-tuiprobe build-tuiprobe lint staticcheck fmt fmt-check fuzz clean all

default: build

build:
	@mkdir -p $(BINDIR)
	$(GOBUILD) -o $(BINDIR)/$(NAME) .

run: build
	./$(BINDIR)/$(NAME) $(PROMPT)

test:
	@go test ./... -count=1 2>&1; status=$$?; \
	if [ $$status -eq 0 ]; then \
		echo "=== ALL TESTS PASSED ==="; \
	else \
		echo "=== TESTS FAILED (exit $$status) ==="; \
	fi; \
	exit $$status

# Race detector run. Any data race fails the build.
test-race:
	go test -race ./... -count=1

# Repeat the suite in one process to catch leaked global state between runs.
test-repeat:
	go test ./... -count=3

# Language server used by the gated LSP integration tests. Keep this in step
# with the flake's pkgs.gopls (0.23.0).
GOPLS_VERSION ?= v0.23.0

install-gopls:
	go install golang.org/x/tools/gopls@$(GOPLS_VERSION)

# The second language server the gated tests exercise (issue #10). TypeScript 5 is
# pinned on purpose: tsserver resolves `typescript` from the workspace, and
# versions 6+ restructured the package (no lib/tsserver.js), which
# typescript-language-server refuses with "Could not find a valid TypeScript
# installation". The test links the installation that sits next to the server into
# its fixture, which is where a global npm install puts it.
TSLS_VERSION ?= latest
install-tsls:
	npm install -g typescript@5 typescript-language-server@$(TSLS_VERSION)

# The LSP integration tests spawn a real language server, so gopls must be on
# PATH (the Nix devShell provides it) and LSP_TEST must be set to un-skip them.
test-lsp:
	# -v on purpose: which server was exercised, and which case skipped with which
	# reason, is the point of this target — a silent skip is how the second server
	# stayed unverified (issue #10).
	LSP_TEST=1 go test -count=1 -v ./lsp/...

# Real-browser smoke tests: renders a local JavaScript page through both browser
# paths and asserts the filtering proxy was used (TestBrowserSmokeThroughProxy),
# then serves a page with a scoped loopback exemption and asserts a subresource on
# a second, live loopback service is refused by the proxy
# (TestBrowserSmokeRefusesABlockedSubresource). Needs a Chromium/Chrome (system
# install or the Playwright cache); skipped without BROWSER_TEST=1 so an ordinary
# `make test` never launches a browser.
test-browser:
	BROWSER_TEST=1 go test -count=1 -timeout 5m -run TestBrowserSmoke ./tool/

# On-demand visual check for the TUI: renders the committed frame scenarios to
# PNGs (Chromium), runs the built binary on a real 80x24 PTY and again on a PTY
# whose window size was never set (the 0x0 report), replays the live terminal
# stream into a screen buffer and screenshots that too, then asserts the streams
# carried styling and the runs quit cleanly. Needs `bin/tinycode` (built here), a
# Chromium (system install or the Playwright cache) and a PTY; everything is
# skipped without TUI_SHOT=1, so an ordinary `make test` needs neither. PNGs
# land in TUI_SHOT_DIR (default /tmp) for a reviewer or an agent to open.
test-tui-visual: build
	TUI_SHOT=1 go test -count=1 -timeout 10m -run 'TestFrameScreenshots|TestBinary|TestParity' ./tui/

# Blocking lint: `go vet` failures fail the build.
lint:
	go vet ./...

# Stricter linter, pinned so local runs match CI. Deliberately separate from
# `lint` so a missing tool can never turn that target into a no-op.
STATICCHECK_VERSION ?= v0.8.1

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

# tuiprobe is a nested module: the root's ./... does NOT see it (measured: go
# list/build/vet ./... all skip it silently), so it needs its own targets and its
# own CI steps. gofmt -l . from the root does cover it.
TUIPROBE_DIR = tuiprobe

test-tuiprobe:
	cd $(TUIPROBE_DIR) && go vet ./... && go test ./... -count=1

test-tuiprobe-race:
	cd $(TUIPROBE_DIR) && go test ./... -count=1 -race

lint-tuiprobe:
	cd $(TUIPROBE_DIR) && go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

build-tuiprobe:
	@mkdir -p $(BINDIR)
	cd $(TUIPROBE_DIR) && go build -o ../$(BINDIR)/tuiprobe ./cmd/tuiprobe

# Format the tracked Go sources in place.
fmt:
	gofmt -w $$(git ls-files '*.go')

# Fail when a tracked Go file is not gofmt-clean.
fmt-check:
	@files="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$files" ]; then \
		echo "=== gofmt required for: ==="; echo "$$files"; exit 1; \
	fi

clean:
	rm -rf $(BINDIR)

# ---- Cross-compilation ----

linux-amd64:
	@mkdir -p $(BINDIR)
	GOARCH=amd64 GOOS=linux $(GOBUILD) -o $(BINDIR)/$(NAME)-$@

linux-arm64:
	@mkdir -p $(BINDIR)
	GOARCH=arm64 GOOS=linux $(GOBUILD) -o $(BINDIR)/$(NAME)-$@

darwin-arm64:
	@mkdir -p $(BINDIR)
	GOARCH=arm64 GOOS=darwin $(GOBUILD) -o $(BINDIR)/$(NAME)-$@

all: $(PLATFORM_LIST)
	@echo "Built all platforms: $(PLATFORM_LIST)"

# Release builds: cross-compile all platforms and create compressed archives
gz_releases = $(addsuffix .tar.gz, $(PLATFORM_LIST))
zip_releases = $(addsuffix .zip, $(PLATFORM_LIST))

%.tar.gz: %
	tar czf $(BINDIR)/$(NAME)-$*.tar.gz -C $(BINDIR) $(NAME)-$*
	rm -f $(BINDIR)/$(NAME)-$*

%.zip: %
	cd $(BINDIR) && zip $(NAME)-$*.zip $(NAME)-$*
	rm -f $(BINDIR)/$(NAME)-$*

releases: $(gz_releases)
	@echo "Release archives: $(gz_releases)"

# Fuzz targets, as "<package>:<function>". `go test -fuzz` runs exactly one
# target per invocation, so this loops over the list. The seed corpus of every
# target already runs as part of `make test`; this explores further.
FUZZTIME ?= 30s
FUZZ_TARGETS = \
	./tool:FuzzFuzzyFindInvariants \
	./tool:FuzzCorrectIndentation \
	./tool:FuzzLevenshtein \
	./tool:FuzzRelBeneath \
	./tui:FuzzWordWrapPreservesWords \
	./tui:FuzzParseMarkdown \
	./mcp:FuzzReadMessageBounds \
	./internal/netsafe:FuzzIsBlockedIP \
	./internal/netsafe:FuzzNormalizeAuthority \
	./internal/browserproxy:FuzzProxyHandlesArbitraryTargets

fuzz:
	@for target in $(FUZZ_TARGETS); do \
		pkg=$${target%%:*}; fn=$${target##*:}; \
		echo "=== $$fn ($$pkg) for $(FUZZTIME)"; \
		go test -run=^$$ -fuzz=^$$fn$$ -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
	done
