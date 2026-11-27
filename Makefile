BINARY  := btctl
PREFIX  ?= /usr/local
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test lint vet fmt fmt-check dist install uninstall clean

all: lint test build

## build: build ./btctl for this machine
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

## test: run the tests with the race detector (needs dbus-daemon)
test:
	$(GO) test -race ./...

## lint: gofmt, go vet, and staticcheck when it is installed
lint: fmt-check vet
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; \
	else echo "staticcheck not installed; skipping (CI runs it)"; fi

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "needs gofmt:"; echo "$$unformatted"; exit 1; fi

## dist: build release archives for every platform into dist/
dist:
	scripts/build-release.sh $(VERSION)

## install: install btctl into $(DESTDIR)$(PREFIX)/bin
install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)

clean:
	rm -rf $(BINARY) dist
