BINARY := ultra-zen
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build test lint vet clean install release

all: vet build

build:
	go build -ldflags="-X main.Version=$(VERSION)" -o $(BINARY) ./cmd/$(BINARY)

install:
	go install -ldflags="-X main.Version=$(VERSION)" ./cmd/$(BINARY)

test:
	go test ./... -count=1 -race

vet:
	go vet ./...

lint:
	golangci-lint run

clean:
	rm -f $(BINARY)
	rm -rf dist/

release:
	goreleaser release --clean
