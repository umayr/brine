# syntax=docker/dockerfile:1

# --- web assets -------------------------------------------------------------
# Built on the build platform (arch-independent), then copied into the Go build.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS web
WORKDIR /src
COPY package.json package-lock.json tailwind.config.js ./
COPY internal/web/assets ./internal/web/assets
RUN npm ci && npm run build:web

# --- brine binary -----------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/web/static ./internal/web/static
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/brine ./cmd/brine

# --- runtime ----------------------------------------------------------------
# Debian trixie (glibc 2.41) is required by the amd64 Dill release binaries,
# which need GLIBC_2.38.
FROM debian:trixie-slim
ARG TARGETARCH
ARG DILL_VERSION=0.4.0
ARG PKL_VERSION=0.31.1

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git curl \
 && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) pkl_arch=amd64 ;; \
      arm64) pkl_arch=aarch64 ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/dill \
      "https://github.com/umayr/dill/releases/download/v${DILL_VERSION}/dill-linux-${TARGETARCH}"; \
    curl -fsSL -o /usr/local/bin/pkl \
      "https://github.com/apple/pkl/releases/download/${PKL_VERSION}/pkl-linux-${pkl_arch}"; \
    chmod 0755 /usr/local/bin/dill /usr/local/bin/pkl

COPY --from=build /out/brine /usr/local/bin/brine

ENV DILL_PKL_PATH=/usr/local/bin/pkl
RUN mkdir -p /data
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["brine", "--data-dir", "/data", "--engine", "docker", "--dill", "/usr/local/bin/dill"]
