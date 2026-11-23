.PHONY: build build-agent build-all test lint proto docker install clean dev

VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

GO := go
GOFLAGS := -trimpath

## Build

build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/monstack ./cmd/monstack

build-agent:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/monstack-agent ./cmd/agent

build-all: build build-agent

cross-compile:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/monstack-linux-amd64 ./cmd/monstack
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/monstack-linux-arm64 ./cmd/monstack
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/monstack-darwin-arm64 ./cmd/monstack
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/monstack-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/monstack-agent-linux-arm64 ./cmd/agent

## Development

dev:
	$(GO) run ./cmd/monstack server --config configs/examples/monstack.yaml

## Testing

test:
	$(GO) test -race -cover -count=1 ./...

test-verbose:
	$(GO) test -race -cover -count=1 -v ./...

test-integration:
	$(GO) test -race -tags=integration -count=1 ./...

## Code Quality

lint:
	golangci-lint run ./...

fmt:
	gofumpt -l -w .
	$(GO) mod tidy

vet:
	$(GO) vet ./...

## Protobuf

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/proto/*.proto

## Docker

docker:
	docker build -t monstack:$(VERSION) -f deployments/docker/Dockerfile .

docker-compose:
	docker compose -f deployments/docker/docker-compose.yml up -d

## Install

install: build-all
	install -m 755 bin/monstack /usr/local/bin/
	install -m 755 bin/monstack-agent /usr/local/bin/
	mkdir -p /etc/monstack /var/lib/monstack /var/log/monstack
	test -f /etc/monstack/monstack.yaml || cp configs/examples/monstack.yaml /etc/monstack/
	test -f /etc/systemd/system/monstack.service || cp deployments/systemd/monstack.service /etc/systemd/system/
	systemctl daemon-reload

## Cleanup

clean:
	rm -rf bin/
	$(GO) clean -cache -testcache
