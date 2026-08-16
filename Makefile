PLUGIN_BIN := dist/plugins/nomad-driver-steamcmd

.PHONY: build test lint vet clean dev-agent

build:
	mkdir -p dist/plugins
	go build -o $(PLUGIN_BIN) ./cmd/nomad-driver-steamcmd

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
