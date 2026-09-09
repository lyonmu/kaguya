.PHONY: build backend frontend install clean test docker default

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

FRONTEND_EMBED_DIR := internal/api/v1/system/frontend

.PHONY: frontend
frontend:
	cd web && bun install --frozen-lockfile
	cd web && bun run build
	rm -rf $(FRONTEND_EMBED_DIR)
	mkdir -p $(FRONTEND_EMBED_DIR)
	cp -R web/dist/. $(FRONTEND_EMBED_DIR)/

.PHONY: backend
backend:
	mkdir -p target
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ./target/$(PROJECT_NAME) main.go

.PHONY: build
build: frontend
	mkdir -p target
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o ./target/$(PROJECT_NAME) main.go

install: build
	install -m 0755 ./target/$(PROJECT_NAME) ~/.local/bin/$(PROJECT_NAME)

.PHONY: docker
docker:
	docker build -t $(PROJECT_NAME):$(VERSION) .
	docker tag $(PROJECT_NAME):$(VERSION) $(PROJECT_NAME):latest

.PHONY: test
test:
	go test -race -count=1 ./...

.PHONY: clean
clean:
	rm -rf target