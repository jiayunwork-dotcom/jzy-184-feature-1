# syntax=docker/dockerfile:1
# ---- 构建阶段 ----
FROM golang:1.22-alpine AS build

WORKDIR /src

# 先拷依赖清单，利用层缓存
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# 静态编译（CGO_ENABLED=0，便于放进精简镜像）
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=build /out/server /app/server
USER appuser

EXPOSE 8080
ENTRYPOINT ["/app/server"]
