GO ?= go
BINARY ?= anvil

.PHONY: build test verify

build:
	$(GO) build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o $(BINARY) ./src

test:
	$(GO) test ./...

verify:
	$(GO) test ./...
	$(GO) vet ./...
