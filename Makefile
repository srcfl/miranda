# terminal-relay — local dev

PREFIX ?= $(HOME)/.local/bin

.PHONY: build test race install dev

build:
	cd go && go build -o ../bin/mir-signal ./cmd/mir-signal
	cd go && go build -o ../bin/mir-agent ./cmd/mir-agent
	cd go && go build -o ../bin/mir ./cmd/mir

test:
	cd go && go test ./...

race:
	cd go && go test -race -count=1 ./...

# install the CLIs onto your PATH (default ~/.local/bin; override with PREFIX=...)
install: build
	mkdir -p "$(PREFIX)"
	install -m 0755 bin/mir bin/mir-agent bin/mir-signal "$(PREFIX)/"
	@echo "installed mir, mir-agent, mir-signal -> $(PREFIX)"

# dev: run the signaling server + an agent locally.
# 1) `make build`
# 2) `make dev` (starts mir-signal, enrolls an agent against it)
# Pair an owner with: bin/mir pair-dev --owner-pub <hex>
dev: build
	@echo "starting mir-signal on :8443 ..."
	@./bin/mir-signal --addr :8443 & echo $$! > /tmp/mir-signal.pid
	@sleep 1
	@./bin/mir enroll --dir /tmp/mir-agent-dev --signal http://localhost:8443 || true
	@echo "agent enrolled. Pair an owner, then: ./bin/mir up --dir /tmp/mir-agent-dev --shell sh"
	@echo "client: ./bin/mir attach <machine>   |   stop signal: kill \`cat /tmp/mir-signal.pid\`"
