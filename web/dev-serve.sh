#!/usr/bin/env bash
# Serve the web/ SPA locally. http://localhost is a WebAuthn secure context, so
# passkeys work in dev (RP-ID = localhost, a disposable identity).
cd "$(dirname "$0")"
PORT="${PORT:-8000}"
echo "serving web/ at http://localhost:$PORT/"
echo "  self-test: http://localhost:$PORT/src/selftest.html"
exec python3 -m http.server "$PORT"
