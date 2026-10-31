# ParseDock control plane: one binary, three roles (coordinator | api | worker).
# Build:  docker build -f docker/Dockerfile -t parsedock:0.1.0 .
# syntax=docker/dockerfile:1.7

# Base images are pinned by digest (images.lock.json); scripts/verify-pins.sh
# fails the build when a Dockerfile, workflow or go.mod drifts from the lock.
FROM golang:1.24.4-bookworm@sha256:10f549dc8489597aa7ed2b62008199bb96717f52a8e8434ea035d5b44368f8a6 AS build
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
# -buildvcs=false keeps the binary identical regardless of the checkout metadata;
# -trimpath removes local paths. Together with the pinned toolchain this makes the
# binary reproducible for the same source tree.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
      -ldflags="-s -w -buildid=" -o /out/docparse ./cmd/docparse

FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 10001 --user-group --no-create-home --shell /usr/sbin/nologin parsedock \
 && mkdir -p /data /work \
 && chown -R 10001:10001 /data /work
COPY --from=build /out/docparse /usr/local/bin/docparse
USER 10001:10001
ENV PARSEDOCK_DATA_DIR=/data \
    PARSEDOCK_WORK_DIR=/work \
    PARSEDOCK_LOG_LEVEL=info
# 8080 is the public API. 8081 is the coordinator's INTERNAL listener (/v1 for the
# gateway, /internal/* for workers and operators): documented for tooling, but it must
# never be published to a public interface — keep it on a private network, behind the
# internal/worker tokens and, across hosts, mutual TLS (docs/DEPLOYMENT.md §6c).
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/docparse"]
CMD ["--role", "standalone", "--listen", "0.0.0.0:8080", "--internal-listen", "0.0.0.0:8081"]
