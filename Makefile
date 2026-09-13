.PHONY: build backend frontend install clean test docker default native avatars

default: build

PROJECT_NAME = $(shell basename $(CURDIR))
VERSION := $(shell cat VERSION)
COMMIT = $(shell git rev-parse HEAD)
BRANCH = $(shell git branch --show-current)

# macOS Desktop 发行包
APP_NAME := Kaguya
APP_BUNDLE := $(APP_NAME).app
# Wails 3 预发布依赖使用 production 构建标签：关闭 dev server 与调试行为。
PRODUCTION_TAG := production
# 发布支持的最低系统版本（Info.plist 与 C 部署目标保持一致）。
MACOS_DEPLOYMENT_TARGET ?= 14.0
# 签名与公证的身份是发行环境输入，不写死凭据。
CODESIGN_IDENTITY ?=
NOTARY_PROFILE ?= kaguya

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
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_DEPLOYMENT_TARGET) CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) bash scripts/go-sqlcipher.sh build -tags $(PRODUCTION_TAG) $(LDFLAGS) -o ./target/$(PROJECT_NAME) main.go

.PHONY: build
build: frontend
	$(MAKE) backend

native:
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_DEPLOYMENT_TARGET) bash scripts/build-openssl.sh
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_DEPLOYMENT_TARGET) bash scripts/build-sqlcipher.sh

install: build
	install -m 0755 ./target/$(PROJECT_NAME) ~/.local/bin/$(PROJECT_NAME)

# macOS Desktop：组装 .app 包并做 ad-hoc 临时签名。需要在该架构的原生 macOS 上执行。
.PHONY: package-macos
package-macos: frontend native
	$(MAKE) backend
	MACOS_DEPLOYMENT_TARGET=$(MACOS_DEPLOYMENT_TARGET) bash scripts/package-macos.sh

# 打包可拖拽安装的 DMG（内含 ad-hoc 签名应用，DMG 本身不签名）。
.PHONY: dmg-macos
dmg-macos: package-macos
	bash scripts/dmg-macos.sh

# Developer ID 签名与公证；缺少 CODESIGN_IDENTITY/NOTARY_PROFILE 时明确失败。
.PHONY: sign-macos
sign-macos: package-macos
	CODESIGN_IDENTITY='$(CODESIGN_IDENTITY)' bash scripts/sign-macos.sh sign

.PHONY: notarize-macos
notarize-macos: package-macos
	CODESIGN_IDENTITY='$(CODESIGN_IDENTITY)' NOTARY_PROFILE='$(NOTARY_PROFILE)' bash scripts/sign-macos.sh notarize

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
