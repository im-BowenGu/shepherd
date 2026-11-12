.PHONY: build run fmt lint test clean cross

BINARY=shepherd
GOOS?=linux
GOARCH?=arm64

# Version stamp shown in the startup banner and /api/version.
VERSION?=$(shell cat static/version.txt 2>/dev/null || echo dev)
COMMIT?=$(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_DATE?=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS=-s -w \
	-X github.com/RoboConOxfordshire/shepherd/internal/sysinfo.Version=$(VERSION) \
	-X github.com/RoboConOxfordshire/shepherd/internal/sysinfo.Commit=$(COMMIT) \
	-X github.com/RoboConOxfordshire/shepherd/internal/sysinfo.BuildDate=$(BUILD_DATE)

build:
	go build -o $(BINARY) -trimpath -ldflags="$(LDFLAGS)" ./cmd/shepherd

run: build
	./$(BINARY)

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

test:
	go test ./... -v

clean:
	rm -f $(BINARY)
	rm -rf usercode
	rm -f shepherd/shepherd/static/image.jpg

cross: $(BINARY)

$(BINARY):
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BINARY) -trimpath -ldflags="$(LDFLAGS)" ./cmd/shepherd

.DEFAULT_GOAL := build
