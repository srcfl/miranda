# terminal-relay — local dev

PREFIX ?= $(HOME)/.local/bin

.PHONY: build test race install dev

build:
	cd go && go build -o ../bin/tr-signal ./cmd/tr-signal
	cd go && go build -o ../bin/tr-agent ./cmd/tr-agent
	cd go && go build -o ../bin/trm ./cmd/trm

test:
	cd go && go test ./...

race:
	cd go && go test -race -count=1 ./...

# install the CLIs onto your PATH (default ~/.local/bin; override with PREFIX=...)
install: build
	mkdir -p "$(PREFIX)"
	install -m 0755 bin/trm bin/tr-agent bin/tr-signal "$(PREFIX)/"
	@echo "installed trm, tr-agent, tr-signal -> $(PREFIX)"

# dev: run the signaling server + an agent locally.
# 1) `make build`
# 2) `make dev` (starts tr-signal, enrolls an agent against it)
# Pair an owner with: bin/tr-agent pair-dev --owner-pub <hex>
dev: build
	@echo "starting tr-signal on :8443 ..."
	@./bin/tr-signal --addr :8443 & echo $$! > /tmp/tr-signal.pid
	@sleep 1
	@./bin/tr-agent enroll --dir /tmp/tr-agent-dev --signal http://localhost:8443 || true
	@echo "agent enrolled. Pair an owner, then: ./bin/tr-agent up --dir /tmp/tr-agent-dev --shell sh"
	@echo "client: ./bin/trm attach <machine>   |   stop signal: kill \`cat /tmp/tr-signal.pid\`"
