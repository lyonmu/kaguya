FROM golang:1.27-alpine AS builder

ENV GOPROXY=https://goproxy.cn,direct

WORKDIR /kaguya

COPY . .

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk update \
    && apk upgrade \
    && apk add --no-cache build-base ca-certificates git make \
    && go mod tidy \
    && make build

FROM busybox:musl AS runtime

WORKDIR /kaguya

COPY --from=builder /kaguya/target/kaguya /bin/kaguya

EXPOSE 9024

ENTRYPOINT [ "/bin/kaguya"]
CMD ["--port","9024","--machine-id","924","--router-prefix","/kaguya/api","--db-kind","postgresql","--db.host","127.0.0.1","--db.port","5432","--db.user","pgvector","--db.password","pgvector-123","--db.db_name","kaguya",]