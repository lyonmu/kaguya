.PHONY: build backend frontend install clean test docker default native avatars

default: build

PROJECT_NAME = $(shell basename $(CURDIR))
VERSION := $(shell cat VERSION)
COMMIT = $(shell git rev-parse HEAD)
BRANCH = $(shell git branch --show-current)

# Go build configuration
CGO_ENABLED = 1
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
backend: native
	mkdir -p target
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) bash scripts/go-sqlcipher.sh build $(LDFLAGS) -o ./target/$(PROJECT_NAME) main.go

.PHONY: build
build: frontend
	$(MAKE) backend

native:
	bash scripts/build-sqlcipher.sh

install: build
	install -m 0755 ./target/$(PROJECT_NAME) ~/.local/bin/$(PROJECT_NAME)

.PHONY: docker
docker:
	docker build -t $(PROJECT_NAME):$(VERSION) .
	docker tag $(PROJECT_NAME):$(VERSION) $(PROJECT_NAME):latest

.PHONY: test
test: native
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) bash scripts/go-sqlcipher.sh test -race -count=1 ./...

.PHONY: clean
clean:
	rm -rf target
# 头像衍生图：从 images/ 原图生成 1x/2x 的 WebP，供前端 avatars.ts 引用。
# 需要 cwebp（brew install webp）；原图保留，不进入构建产物。
.PHONY: avatars
avatars:
	cd web/src/assets && cwebp -quiet -q 82 -resize 144 144 kaguya.png -o kaguya-144.webp && cwebp -quiet -q 80 -resize 288 288 kaguya.png -o kaguya-288.webp && cwebp -quiet -q 82 -resize 144 144 lyonmu.png -o lyonmu-144.webp && cwebp -quiet -q 80 -resize 288 288 lyonmu.png -o lyonmu-288.webp
	cd web/src/assets && cwebp -quiet -q 85 -resize 64 64 kaguya.png -o ../../public/kaguya-favicon.webp
