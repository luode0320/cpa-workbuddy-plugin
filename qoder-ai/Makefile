.PHONY: build test lint clean release

GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)

# Default target: build the plugin for the current platform.
build:
	CGO_ENABLED=1 $(GO) build -buildmode=c-shared -ldflags "$(LDFLAGS)" -o qoderwork.so .

# Run all tests with race detector.
test:
	$(GO) test -race -count=1 ./...

# Lint everything (requires gofmt + go vet; staticcheck/gocritic/unparam optional).
lint:
	@test -z "$$($(GO)fmt -l .)" || ($(GO)fmt -l . && exit 1)
	$(GO) vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; fi
	@if command -v gocritic >/dev/null 2>&1; then gocritic check ./...; fi
	@if command -v unparam >/dev/null 2>&1; then unparam ./...; fi

# Clean build artifacts.
clean:
	rm -f qoderwork.so qoderwork.h
	rm -rf bin/ dist/ coverage.out coverage.html

# Cross-compile for all supported platforms (requires cross C toolchains).
# On a Linux host without osxcross, only linux/* targets will succeed.
release: clean
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64; do \
	  os=$${target%/*}; arch=$${target#*/}; \
	  out=dist/qoderwork_$(VERSION)_$${os}_$${arch}; \
	  mkdir -p $$out; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=1 $(GO) build -buildmode=c-shared -ldflags "$(LDFLAGS)" -o $$out/qoderwork.so . 2>&1 | grep -v "cgo is not enabled" || true; \
	  if [ -f $$out/qoderwork.so ]; then \
	    cp README.md README_CN.md LICENSE $$out/; \
	    cd dist && zip -qr qoderwork_$(VERSION)_$${os}_$${arch}.zip qoderwork_$(VERSION)_$${os}_$${arch} && cd ..; \
	    echo "built $$out"; \
	  fi; \
	done

# Tag a new release (usage: make tag VERSION=0.8.0).
tag:
	git tag -a v$(VERSION) -m "v$(VERSION)"
	git push origin v$(VERSION)
