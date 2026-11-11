# syntax=docker/dockerfile:1

FROM node:24-alpine AS frontend-build

WORKDIR /src

COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,id=npm,target=/root/.npm \
    npm ci

COPY web/ .
ARG BUILD_TIMESTAMP
ENV VITE_BUILD_TIMESTAMP=${BUILD_TIMESTAMP}
RUN --mount=type=cache,id=npm,target=/root/.npm \
    npm run build

FROM golang:1.26.11-alpine AS backend-build

WORKDIR /src

COPY server/go.mod server/go.sum ./
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod \
    go mod download

COPY server/ .
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w \
      -X 'xlyra/server/internal/version.Version=${VERSION}' \
      -X 'xlyra/server/internal/version.Commit=${COMMIT}' \
      -X 'xlyra/server/internal/version.BuildTime=${BUILD_TIME}'" \
    -o /out/xlyra-server ./cmd/server

FROM node:22-alpine AS agent-runtime-build

WORKDIR /src/agent-runtime

COPY agent-runtime/package.json agent-runtime/package-lock.json ./
RUN --mount=type=cache,id=npm,target=/root/.npm \
    npm ci --ignore-scripts --omit=dev

COPY agent-runtime/extension ./extension

FROM alpine:3.22

# bash/ripgrep/fd 供 Agent 的 bash 工具与 Pi 的搜索工具使用;libstdc++/libgcc 是
# 从 node 镜像拷来的 node 二进制的运行时依赖;tini 作 PID 1 回收沙箱里的孤儿进程。
RUN apk add --no-cache ca-certificates tzdata tini bash ripgrep fd libstdc++ libgcc
COPY --from=agent-runtime-build /usr/local/bin/node /usr/local/bin/node

WORKDIR /app

ENV APP_ENV=production \
    WORKDIR=/data \
    HTTP_HOST=0.0.0.0 \
    HTTP_PORT=5801 \
    STATIC_DIR=/app/dist \
    AGENT_NODE_BIN=/usr/local/bin/node \
    AGENT_PI_ENTRY=/app/agent-runtime/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js

EXPOSE 5801
VOLUME ["/data"]

COPY --from=backend-build /out/xlyra-server /app/xlyra-server
COPY --from=frontend-build /src/dist /app/dist
COPY --from=agent-runtime-build /src/agent-runtime /app/agent-runtime

ENTRYPOINT ["/sbin/tini", "--", "/app/xlyra-server"]
