# Build on the native platform and cross-compile, so multi-arch builds don't run under emulation
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

ENV CGO_ENABLED=0

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

# Only what the server needs: the native app (main.go, mygo.json, resources)
# stays out, see .dockerignore.
COPY . .
RUN go test ./light/... ./web/...
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /yeelight ./cmd/server \
    && mkdir /config

# Static binary with the page embedded, so scratch is enough
FROM scratch

COPY --from=builder /yeelight /yeelight
# Owned by nobody so a named volume mounted here is writable
COPY --from=builder --chown=65534:65534 /config /config

# nobody
USER 65534:65534

# Where the light's address and the schemes are kept; mount a volume on it.
ENV YEELIGHT_DATA=/config
VOLUME /config

EXPOSE 10000

# scratch has no curl, so the binary checks itself
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/yeelight", "-healthcheck"]

ENTRYPOINT ["/yeelight"]
