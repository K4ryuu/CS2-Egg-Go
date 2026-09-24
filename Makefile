VERSION ?= $(shell cat VERSION 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
CHANNEL ?= dev
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%MZ)
LDFLAGS  = -s -w \
  -X github.com/K4ryuu/CS2-Egg-Go/internal/version.Version=$(VERSION) \
  -X github.com/K4ryuu/CS2-Egg-Go/internal/version.Channel=$(CHANNEL) \
  -X github.com/K4ryuu/CS2-Egg-Go/internal/version.Commit=$(COMMIT) \
  -X github.com/K4ryuu/CS2-Egg-Go/internal/version.Built=$(BUILT)
GOOS    = linux
GOARCH  = amd64

-include .env.local

.PHONY: test vet lint egg node node-dev clean

test:
	go test ./...

vet:
	go vet ./...

lint: vet
	@command -v staticcheck >/dev/null && staticcheck ./... || echo "staticcheck not installed, vet only"

egg:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o dist/cs2egg ./cmd/cs2egg

node:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o dist/cs2node ./cmd/cs2node

# NODE_HOST comes from .env.local (gitignored), e.g. NODE_HOST=game2
node-dev: node
	@test -n "$(NODE_HOST)" || { echo "set NODE_HOST in .env.local"; exit 1; }
	scp dist/cs2node $(NODE_HOST):/usr/local/bin/cs2node.new
	ssh $(NODE_HOST) 'mv /usr/local/bin/cs2node.new /usr/local/bin/cs2node && chmod +x /usr/local/bin/cs2node && (systemctl restart cs2node 2>/dev/null || true) && cs2node version'

clean:
	rm -rf dist
