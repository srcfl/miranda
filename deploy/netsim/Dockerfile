# NAT-sim image: builds the tr binaries, ships them with routing/NAT tooling.
# Build context is the repo root (see docker-compose.yml).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go/ ./go/
WORKDIR /src/go
RUN go build -o /out/mir-signal ./cmd/mir-signal && \
    go build -o /out/mir-agent ./cmd/mir-agent && \
    go build -o /out/mir ./cmd/mir

FROM alpine:3
RUN apk add --no-cache iproute2 iptables tmux ca-certificates
COPY --from=build /out/mir-signal /out/mir-agent /out/mir /usr/local/bin/
COPY deploy/netsim/nat/entrypoint.sh /nat-entrypoint.sh
COPY deploy/netsim/node/entrypoint.sh /node-entrypoint.sh
RUN chmod +x /nat-entrypoint.sh /node-entrypoint.sh
