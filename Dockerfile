# Multi-stage Docker build for MediaVault
FROM golang:1.26-alpine AS builder

WORKDIR /src
RUN apk add --no-cache git ca-certificates tzdata

# 国内环境使用可访问的 Go 模块代理（可通过 --build-arg GOPROXY=... 覆盖）
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w" \
    -o /mediavault ./cmd/server

# Final lightweight production image
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

COPY --from=builder /mediavault /app/mediavault

# Volume for database, logs, and cache
VOLUME ["/app/data"]

EXPOSE 8096

ENV MV_DATA_DIR=/app/data
ENV MV_LISTEN=0.0.0.0:8096

ENTRYPOINT ["/app/mediavault"]
