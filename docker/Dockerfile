# Base images mirrored to ACR (opencsghq) with full upstream multi-arch indexes
# so CI can pull without Docker Hub while preserving linux/amd64 and linux/arm64.
#
ARG RUNTIME_ALPINE_BASE_IMAGE=opencsg-registry.cn-beijing.cr.aliyuncs.com/opencsghq/alpine:3.23

# ============================================================
# Stage 1: Build the picoclaw binary
# ============================================================
FROM --platform=$BUILDPLATFORM opencsg-registry.cn-beijing.cr.aliyuncs.com/opencsghq/golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

# Alpine apk: use Aliyun mirror instead of dl-cdn (often slow or flaky in mainland CN).
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories

RUN apk add --no-cache git make

# Go modules: default proxy/sumdb can be slow or unreachable in CN.
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
ENV GIT_TERMINAL_PROMPT=0

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN set -eux; \
  targetos="${TARGETOS:-linux}"; \
  targetarch="${TARGETARCH:-$(go env GOARCH)}"; \
  case "${targetarch}/${TARGETVARIANT:-}" in \
    arm/v*) export GOARM="${TARGETVARIANT#v}" ;; \
  esac; \
  make build GO="CGO_ENABLED=0 GOOS=${targetos} GOARCH=${targetarch} go" PLATFORM="${targetos}" ARCH="${targetarch}"

# ============================================================
# Stage 1.5: Download platform-specific csgclaw-cli release tarball
# ============================================================
FROM --platform=$BUILDPLATFORM opencsg-registry.cn-beijing.cr.aliyuncs.com/opencsghq/alpine:3.23 AS csgclaw-cli
ARG TARGETARCH
# Override to pin a release, e.g. https://csgclaw.opencsg.com/releases/v1.0.0
ARG CSGCLAW_CLI_BASE_URL=https://csgclaw.opencsg.com/releases/latest
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
  && apk add --no-cache ca-certificates curl \
  && set -eu; \
     arch="${TARGETARCH:-}"; \
     if [ -z "$arch" ]; then \
       case "$(uname -m)" in \
         x86_64) arch=amd64 ;; \
         aarch64) arch=arm64 ;; \
         *) echo "unsupported build arch: $(uname -m)" >&2; exit 1 ;; \
       esac; \
     fi; \
     curl -fSL "${CSGCLAW_CLI_BASE_URL}/linux/${arch}?package=csgclaw-cli" -o /tmp/csgclaw-cli.tar.gz \
  && mkdir -p /tmp/extract \
  && tar -xzf /tmp/csgclaw-cli.tar.gz -C /tmp/extract \
  && set -eu; \
     bin="$(find /tmp/extract -type f \( -name csgclaw-cli -o -name "csgclaw-cli_linux_${arch}" \) | head -n1)"; \
     if [ -z "$bin" ]; then bin="$(find /tmp/extract -type f -perm -111 | head -n1)"; fi; \
     if [ -z "$bin" ]; then echo "csgclaw-cli binary not found in archive"; ls -laR /tmp/extract; exit 1; fi; \
     install -m0755 "$bin" /csgclaw-cli

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM ${RUNTIME_ALPINE_BASE_IMAGE}

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories

RUN apk add --no-cache ca-certificates curl python3 tini tzdata

# Health check (use 127.0.0.1: gateway listens on IPv4 only; "localhost" may resolve to ::1 and fail)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:18790/health || exit 1

# Copy binary
COPY --from=builder /src/build/picoclaw /usr/local/bin/picoclaw
COPY --from=csgclaw-cli /csgclaw-cli /usr/local/bin/csgclaw-cli

# Create non-root user and group
RUN addgroup -g 1000 picoclaw && \
    adduser -D -u 1000 -G picoclaw picoclaw

# Switch to non-root user
USER picoclaw

# Run onboard to create initial directories and config
RUN /usr/local/bin/picoclaw onboard && \
    find /home/picoclaw/.picoclaw/workspace/skills -mindepth 1 -maxdepth 1 \
      ! -name 'manager-worker-dispatch' ! -name 'basics' -exec rm -rf {} +

# Bundle the default config into the image
COPY --chown=picoclaw:picoclaw docker/config/config.json /home/picoclaw/.picoclaw/config.json

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["sleep", "infinity"]

# CMD ["picoclaw", "gateway", "-d"]
