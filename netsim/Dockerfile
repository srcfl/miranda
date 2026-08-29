# One image for every netsim role: the relay, the NAT routers, the agent and the
# driver. Sharing it means one build and one layer cache for the whole matrix.
#
# The driver is a Go *test* binary on purpose. internal/client stores the owner
# root only in the OS keychain and accepts the MIR_TEST_KEYCHAIN_DIR override
# solely when argv[0] ends in ".test", so building it this way lets a headless
# Linux container hold an owner identity without weakening that rule.
FROM golang:1.26-alpine AS build
ENV CGO_ENABLED=0
WORKDIR /src/go
COPY go/go.mod go/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY go/ ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/mir ./cmd/mir && \
    go build -trimpath -o /out/mir-signal ./cmd/mir-signal && \
    go test -c -trimpath -o /out/netsim-driver.test ./internal/netsim

FROM alpine:3.22
# tmux: the agent's real launch command, and what makes continuation across a
# network flip observable. iproute2/iptables: the NAT routers and the uplink flip.
RUN apk add --no-cache tmux iproute2 iptables ca-certificates
COPY --from=build /out/mir /usr/local/bin/mir
COPY --from=build /out/mir-signal /usr/local/bin/mir-signal
COPY --from=build /out/netsim-driver.test /usr/local/bin/netsim-driver.test
COPY netsim/scripts/ /usr/local/bin/
RUN chmod +x /usr/local/bin/netsim-*.sh
ENTRYPOINT ["/usr/local/bin/netsim-node.sh"]
CMD ["sh"]
