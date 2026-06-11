NAME=tinycode
BINDIR=bin
VERSION=$(shell /usr/bin/git --no-pager describe --tags 2>/dev/null || echo "dev")
COMMIT_SHA=$(shell /usr/bin/git --no-pager rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME=$(shell date -u)
GOBUILD=CGO_ENABLED=0 go build -trimpath -ldflags '-X "main.Version=$(VERSION)" \
		-X "main.CommitSHA=$(COMMIT_SHA)" \
		-X "main.BuildTime=$(BUILDTIME)" \
		-w -s -buildid='

PLATFORM_LIST = \
	linux-amd64 \
	linux-arm64 \
	darwin-arm64

.PHONY: default build run test lint clean all

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

lint:
	go vet ./...
	@which staticcheck > /dev/null 2>&1 && staticcheck ./... || true

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
