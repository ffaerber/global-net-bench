# Pinning the build stage to the native platform lets Go cross-compile for the
# target, which is far faster than emulating the target under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies are cached separately so source edits do not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/globalnetbench ./cmd/globalnetbench

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=build /out/globalnetbench /usr/local/bin/globalnetbench
COPY config.example.yaml /etc/globalnetbench/config.example.yaml

# ICMP and traceroute need raw sockets. The compose file drops every other
# capability and grants only NET_RAW.
ENV GNB_CONFIG=/etc/globalnetbench/config.yaml

# Running from /data means the default relative SQLite DSN lands on the volume
# rather than in the container's writable layer, where it would be lost.
WORKDIR /data

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/api/v1/status >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/globalnetbench"]
