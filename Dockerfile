# Stage 1: Build the web frontend from source.
# --platform=$BUILDPLATFORM keeps this stage on the host architecture: the
# bundle is platform-independent JavaScript, so there is no reason to emulate
# node under QEMU when building multi-arch images.
# The `frontend` named build context must point at the Dydan AppWeb repository
# checkout (docker-compose.yaml wires it up automatically; plain docker users
# can pass --build-context frontend=/path/to/daydan-app).
FROM --platform=$BUILDPLATFORM node:22-slim AS frontend-builder

WORKDIR /web

# One source per COPY: this daemon's BuildKit drops extra sources when a
# local named context COPY mixes files and directories.
COPY --from=frontend package.json ./
COPY --from=frontend package-lock.json ./
COPY --from=frontend .npmrc ./
COPY --from=frontend index.html ./
COPY --from=frontend vite.config.mjs ./
COPY --from=frontend vite.allowed-hosts.mjs ./
COPY --from=frontend src ./src
COPY --from=frontend public ./public
COPY --from=frontend patches ./patches

RUN npm ci --no-audit --no-fund

# Plain `npm run build` produces a same-origin (relative `/api`) bundle that is
# safe for any self-hosted IP/hostname/reverse proxy without rebuilding.
RUN npm run build

# Stage 2: Build the application from source.
# --platform=$BUILDPLATFORM pins this stage to the host (native) architecture
# even when building for other target platforms, so `go build` cross-compiles
# natively via GOOS/GOARCH instead of running under QEMU emulation. Emulated
# `go build` for arm64/armv7 is drastically slower than native cross-compiling
# a pure-Go (CGO_ENABLED=0) binary -- this matters for the multi-arch
# (linux/amd64,arm64,arm/v7) image built in go-release.yml.
FROM --platform=$BUILDPLATFORM golang:1.25.13-alpine AS builder

WORKDIR /usr/src/app

RUN apk --no-cache add ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# Overwrite the tracked placeholder in frontend/dist with the freshly built
# frontend so `go:embed` bakes the real SPA into the binary.
COPY --from=frontend-builder /web/dist ./frontend/dist

# TARGETOS/TARGETARCH are populated automatically by buildkit for the actual
# target platform being built, regardless of the native host running this stage.
ARG TARGETOS
ARG TARGETARCH
# VERSION/COMMIT are baked into config.Version/config.Commit via ldflags so
# `/api/v1/resource` and `/health` report the exact build that's running.
# Passing neither is fine for local/dev builds (falls back to "dev").
ARG VERSION=dev
ARG COMMIT=dev
RUN --mount=type=tmpfs,target=/tmp \
    --mount=type=tmpfs,target=/root/.cache/go-build \
    BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w \
      -X github.com/alamoudimoh/dydan-server/config.Version=${VERSION} \
      -X github.com/alamoudimoh/dydan-server/config.Commit=${COMMIT} \
      -X github.com/alamoudimoh/dydan-server/config.BuildDate=${BUILD_DATE}" \
      -buildvcs=false -o /dydan . && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" \
      -buildvcs=false -o /dydanctl ./cmd/dydanctl

# Stage 3: Create a smaller runtime image
FROM alpine:3.21

# Install necessary CA certificates and timezone data, plus su-exec for the
# entrypoint's privilege drop (tiny, static, and signal-transparent).
RUN apk --no-cache add ca-certificates libc6-compat tzdata su-exec

# Dedicated unprivileged runtime user. The entrypoint starts as root only to
# migrate ownership of pre-existing /data contents (volumes created by earlier
# root-running images), then execs Dydan as this user.
RUN addgroup -g 1000 dydan && adduser -D -H -u 1000 -G dydan dydan

# Copy the self-contained binary. The frontend is embedded at build time.
COPY --from=builder /dydan /dydan
RUN ln -sf /dydan /daydan && ln -sf /dydan /donetick

# Operator toolkit (backup / verify / restore / prune).
COPY --from=builder /dydanctl /usr/local/bin/dydanctl
RUN ln -sf /usr/local/bin/dydanctl /usr/local/bin/daydanctl

COPY docker-entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh

# Dydan keeps its SQLite database, generated JWT secret, and local uploads
# under one persistent directory. No configuration file is required.
ENV DYDAN_DATA_DIR="/data"
VOLUME ["/data"]

# Release provenance (populated by go-release.yml; "unknown"/"dev" for plain
# docker/compose builds). The pairing of server commit/version and the exact
# daydan-app commit the embedded frontend came from is always discoverable
# via these immutable OCI labels. VERSION/COMMIT are re-declared here because
# build args do not cross stage boundaries.
ARG DYDAN_FRONTEND_COMMIT=unknown
ARG DYDAN_FRONTEND_REF=unknown
ARG VERSION=dev
ARG COMMIT=dev
LABEL com.dydan.frontend.commit=${DYDAN_FRONTEND_COMMIT} \
      com.dydan.frontend.ref=${DYDAN_FRONTEND_REF} \
      org.opencontainers.image.revision=${COMMIT} \
      org.opencontainers.image.version=${VERSION} \
      org.opencontainers.image.source=https://github.com/alamoudimoh/daydan-server

# Expose the application port
EXPOSE 2021

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost:2021/api/v1/health || exit 1

# Run through the privilege-dropping entrypoint; the Go binary is exec'd as
# the unprivileged "dydan" user (uid/gid 1000) with PID 1 semantics intact.
ENTRYPOINT ["/entrypoint.sh"]

# Command to run the application
CMD ["/dydan"]
