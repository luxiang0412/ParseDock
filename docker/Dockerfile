# ParseDock control plane: one binary, three roles (coordinator | api | worker).
# Build:  docker build -f docker/Dockerfile -t parsedock:0.1.0 .
# syntax=docker/dockerfile:1.7

FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/docparse ./cmd/docparse

FROM debian:trixie-slim AS runtime
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
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/docparse"]
CMD ["--role", "standalone", "--listen", "0.0.0.0:8080", "--internal-listen", "0.0.0.0:8081"]
