# syntax=docker/dockerfile:1
# Test-only hub image for the agent integration test (no embedded SPA).
ARG GO_VERSION=1.27.3
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/asterism ./cmd/asterism

FROM alpine:3.20
COPY --from=build /out/asterism /usr/local/bin/asterism
USER 65532:65532
EXPOSE 7700
ENTRYPOINT ["/usr/local/bin/asterism"]
CMD ["serve", "--config", "/etc/asterism/hub.toml"]
