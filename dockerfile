FROM oven/bun:1-alpine AS frontend-builder

ENV BUN_CONFIG_REGISTRY=https://registry.npmmirror.com

WORKDIR /kaguya/web

COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile

COPY web/ ./
RUN bun run build

FROM golang:1.27-alpine AS builder

ENV GOPROXY=https://goproxy.cn,direct

WORKDIR /kaguya

# SQLCipher needs a C toolchain, pkg-config and a static OpenSSL libcrypto.a;
# the Makefile scripts also require bash, curl, tar, make and git.
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache bash build-base ca-certificates curl git make openssl openssl-dev openssl-libs-static pkg-config

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend-builder /kaguya/web/dist ./internal/api/v1/system/frontend

RUN make backend

# Alpine keeps BusyBox (pidof for health checks) and provides Bash for the agent tool.
FROM alpine:3.22 AS runtime

RUN apk add --no-cache bash ca-certificates

WORKDIR /kaguya

COPY --from=builder /kaguya/target/kaguya /bin/kaguya

EXPOSE 9024

ENTRYPOINT [ "/bin/kaguya" ]
# The container listens on all interfaces; publish the port only on the host loopback.
CMD ["--host","0.0.0.0","--port","9024","--machine-id","924","--router-prefix","/kaguya/api","--db.path","/data/kaguya.db","--db.key-file","/run/secrets/kaguya.key"]
