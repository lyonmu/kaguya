.PHONY: build clean test

default: build

PROJECT_NAME = $(shell basename $(CURDIR))
VERSION := $(shell cat VERSION)
COMMIT = $(shell git rev-parse HEAD)
BRANCH = $(shell git branch --show-current)

# Go build configuration
CGO_ENABLED ?= 0
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# -s -w: strip debug info and symbol table for smaller binary
# -X: inject version info at link time
LDFLAGS = -ldflags "-s -w \
	-X 'github.com/lyonmu/gopkg/version.Version=${VERSION}' \
	-X 'github.com/lyonmu/gopkg/version.Commit=${COMMIT}' \
	-X 'github.com/lyonmu/gopkg/version.Branch=${BRANCH}'"

.PHONY: build
build:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH)  go build $(LDFLAGS) -o ./target/$(PROJECT_NAME) main.go

.PHONY: test
test:
	go test -race -count=1 ./...

.PHONY: clean
clean:
	rm -rf target