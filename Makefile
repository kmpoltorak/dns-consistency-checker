BINARY   := dns-consistency-checker
PKG      := github.com/kmpoltorak/dns-consistency-checker
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X $(PKG)/internal/cli.Version=$(VERSION)
GOLANGCI ?= golangci-lint
GOVULNCHECK_VERSION ?= v1.8.0
ARGS     ?= help

.PHONY: build test test-race test-integration fmt fmt-check vet lint vulncheck run clean docker-build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

test-race:
	go test -race ./...

# Integration tests run the whole CLI against local in-process DNS servers.
test-integration:
	go test -count=1 -run 'Integration' -v ./internal/cli/

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "files above need gofmt"; exit 1)

vet:
	go vet ./...

lint:
	$(GOLANGCI) run ./...

# Reports known vulnerabilities in reachable code of dependencies and the Go toolchain.
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Example: make run ARGS="check --host example.com --server 1.1.1.1"
run: build
	./bin/$(BINARY) $(ARGS)

clean:
	rm -rf bin

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) -t $(BINARY):latest .
