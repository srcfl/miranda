# NAT-sim image: builds the tr binaries, ships them with routing/NAT tooling.
# Build context is the repo root (see docker-compose.yml).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go/ ./go/
WORKDIR /src/go
RUN go build -o /out/tr-signal ./cmd/tr-signal && \
    go build -o /out/tr-agent ./cmd/tr-agent && \
    go build -o /out/trm ./cmd/trm

FROM alpine:3
RUN apk add --no-cache iproute2 iptables tmux ca-certificates
COPY --from=build /out/tr-signal /out/tr-agent /out/trm /usr/local/bin/
COPY deploy/netsim/nat/entrypoint.sh /nat-entrypoint.sh
COPY deploy/netsim/node/entrypoint.sh /node-entrypoint.sh
RUN chmod +x /nat-entrypoint.sh /node-entrypoint.sh
