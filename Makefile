# terminal-relay — local dev

.PHONY: build test race dev

build:
	cd go && go build -o ../bin/tr-signal ./cmd/tr-signal
	cd go && go build -o ../bin/tr-agent ./cmd/tr-agent
	cd go && go build -o ../bin/tr ./cmd/tr

test:
	cd go && go test ./...

race:
	cd go && go test -race -count=1 ./...

# dev: run the signaling server + an agent locally.
# 1) `make build`
# 2) `make dev` (starts tr-signal, enrolls + runs an agent against it)
# Pair an owner the web client will use with: bin/tr-agent pair-dev --owner-pub <hex>
dev: build
	@echo "starting tr-signal on :8443 ..."
	@./bin/tr-signal --addr :8443 & echo $$! > /tmp/tr-signal.pid
	@sleep 1
	@./bin/tr-agent enroll --dir /tmp/tr-agent-dev --signal http://localhost:8443 || true
	@echo "agent enrolled. Pair an owner, then: ./bin/tr-agent up --dir /tmp/tr-agent-dev --shell sh"
	@echo "stop the signaling server with: kill \`cat /tmp/tr-signal.pid\`"
