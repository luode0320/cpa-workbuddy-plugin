GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
VERSION ?= $(shell tr -d ' \n\r' < VERSION)

ifeq ($(GOOS),darwin)
	EXT := .dylib
else ifeq ($(GOOS),windows)
	EXT := .dll
else
	EXT := .so
endif

OUT_DIR := dist/plugins/$(GOOS)/$(GOARCH)
OUT_FILE := $(OUT_DIR)/gemini-web2api$(EXT)
CPA_PLUGIN_DIR ?= $(HOME)/.cli-proxy-api/plugins/$(GOOS)/$(GOARCH)

.PHONY: all build test install clean

all: build

build:
	mkdir -p $(OUT_DIR)
	CGO_ENABLED=1 \
	go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -buildmode=c-shared -o $(OUT_FILE) .

test:
	CGO_ENABLED=1 \
	go vet ./... && go test -v ./...

install: build
	mkdir -p "$(CPA_PLUGIN_DIR)"
	cp -f $(OUT_FILE) "$(CPA_PLUGIN_DIR)/gemini-web2api$(EXT)"

clean:
	rm -rf dist
