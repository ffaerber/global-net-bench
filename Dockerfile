FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies are cached separately so source edits do not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=0.1.0
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/globalnetbench ./cmd/globalnetbench

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    mkdir -p /data

COPY --from=build /out/globalnetbench /usr/local/bin/globalnetbench
COPY config.example.yaml /etc/globalnetbench/config.example.yaml

# ICMP and traceroute need raw sockets. The compose file drops every other
# capability and grants only NET_RAW.
ENV GNB_CONFIG=/etc/globalnetbench/config.yaml

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/api/v1/status >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/globalnetbench"]
