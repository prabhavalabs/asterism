# syntax=docker/dockerfile:1
#
# asterism-agent container image: a static binary on scratch, non-root.
#
#   docker buildx build -f deploy/agent/Dockerfile \
#     --platform linux/amd64,linux/arm64 --build-arg VERSION=v0.2.0 \
#     -t ghcr.io/prabhavalabs/asterism-agent:0.2 .
#
# Build from the repository root (the context). `make agent-image` builds
# the host architecture only and loads it into the local Docker engine.
ARG GO_VERSION=1.27.3

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# Extra build tags, e.g. EXTRA_TAGS=pprof for a profiling image (never released).
ARG EXTRA_TAGS=""
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/asterism-agent ./cmd/asterism-agent
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -tags "netgo,osusergo${EXTRA_TAGS:+,$EXTRA_TAGS}" -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/asterism-agent ./cmd/asterism-agent

FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="asterism-agent" \
      org.opencontainers.image.description="Outbound-only telemetry agent for the Asterism VPS observatory" \
      org.opencontainers.image.source="https://github.com/prabhavalabs/asterism" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      asterism.self="true"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/asterism-agent /asterism-agent
USER 65532:65532
ENV GOMAXPROCS=1 GOMEMLIMIT=12MiB GOGC=50 ASTERISM_AGENT_HOST_ROOT=/host
ENTRYPOINT ["/asterism-agent"]
CMD ["run", "--config", "/etc/asterism/agent.yaml"]
