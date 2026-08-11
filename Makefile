BINARY := ultra-zen
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build test lint vet clean install system release

all: vet build

build:
	go build -ldflags="-X main.Version=$(VERSION)" -o $(BINARY) ./cmd/$(BINARY)

install:
	go install -ldflags="-X main.Version=$(VERSION)" ./cmd/$(BINARY)

# system installs to /usr/local/bin (via sudo) and sets up the shared key
# store at /etc/ultra-zen/keys so any user on the machine can launch ultra-zen.
system:
	go build -ldflags="-X main.Version=$(VERSION)" -o $(BINARY) ./cmd/$(BINARY)
	sudo install -m 0755 $(BINARY) /usr/local/bin/$(BINARY)
	sudo /usr/local/bin/$(BINARY) setup --copy-keys

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
