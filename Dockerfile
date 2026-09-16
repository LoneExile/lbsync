# Multi-arch WITHOUT emulation: the builder is pinned to the native build
# platform and cross-compiles for the target. Go can do this because
# CGO_ENABLED=0, so there is no C toolchain to run — and it avoids QEMU, which
# reliably crashes the Go toolchain (GC panics in runtime.gcMarkDone) when the
# amd64 toolchain is emulated on an arm64 host.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/lbsync .

# Empty filesystem: no shell, no package manager, no dependencies.
FROM scratch
LABEL org.opencontainers.image.title="lbsync" \
      org.opencontainers.image.description="Sync ListenBrainz generated playlists into a Subsonic/Navidrome server" \
      org.opencontainers.image.source="https://github.com/LoneExile/lbsync" \
      org.opencontainers.image.licenses="MIT"
# Needed to reach api.listenbrainz.org over TLS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/lbsync /lbsync
USER 65534:65534
ENTRYPOINT ["/lbsync"]
