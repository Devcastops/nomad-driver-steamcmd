PLUGIN_BIN := dist/plugins/nomad-driver-steamcmd
VERSION    ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo v0.0.0-dev)
LDFLAGS    := -X github.com/byteford/nomad-driver-steamcmd/driver.pluginVersion=$(VERSION)

.PHONY: build test lint vet clean dev-agent release-build

build:
	mkdir -p dist/plugins
	go build -ldflags "$(LDFLAGS)" -o $(PLUGIN_BIN) ./cmd/nomad-driver-steamcmd

test:
	go test ./... -race -v -coverprofile=coverage.out

vet:
	go vet ./...

lint: vet
	gofmt -l .

clean:
	rm -rf dist coverage.out /tmp/nomad-data

# Runs a single-node dev agent with the plugin loaded, for local iteration
# against a real Nomad control plane without touching the devcastops
# cluster. Requires steamcmd on PATH.
dev-agent: build
	mkdir -p /tmp/nomad-data
	nomad agent -dev \
		-data-dir=/tmp/nomad-data \
		-plugin-dir=$(CURDIR)/dist/plugins \
		-config=./example/dev-agent.hcl

# Cross-compiles for the platforms the release workflow publishes
# (linux/amd64 for the main devcastops node, linux/arm64 for the
# Raspberry Pi node), version-stamped from the current git tag. Useful
# for testing a build directly on the Pi without waiting on CI.
# Usage: make release-build VERSION=v0.2.0
release-build:
	mkdir -p dist/release
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" \
		-o dist/release/nomad-driver-steamcmd_$(VERSION)_linux_amd64 \
		./cmd/nomad-driver-steamcmd
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" \
		-o dist/release/nomad-driver-steamcmd_$(VERSION)_linux_arm64 \
		./cmd/nomad-driver-steamcmd
	@echo "built:"
	@ls -la dist/release
