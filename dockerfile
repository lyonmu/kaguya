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

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates git make

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend-builder /kaguya/web/dist ./internal/api/v1/system/frontend

RUN make backend

# Compose 的 CMD-SHELL / pidof 健康检查依赖 BusyBox，不能直接替换为 scratch。
FROM busybox:musl AS runtime

WORKDIR /kaguya

COPY --from=builder /kaguya/target/kaguya /bin/kaguya
# busybox:musl 不包含系统根证书，Go HTTPS 客户端需要该文件校验上游模型服务证书。
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

EXPOSE 9024

ENTRYPOINT [ "/bin/kaguya" ]
CMD ["--port","9024","--machine-id","924","--router-prefix","/kaguya/api","--db.kind","postgresql","--db.host","127.0.0.1","--db.port","5432","--db.user","pgvector","--db.password","pgvector-123","--db.db_name","kaguya"]
