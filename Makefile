GO ?= go

.PHONY: build test run-server run-client clean

build:
	mkdir -p bin
	$(GO) build -o bin/gas-tcp-client ./cmd/client
	$(GO) build -o bin/gas-tcp-server ./cmd/server

test:
	$(GO) test ./...

run-server:
	$(GO) run ./cmd/server

run-client:
	$(GO) run ./cmd/client --relay-url "$$RELAY_URL"

clean:
	rm -rf bin
