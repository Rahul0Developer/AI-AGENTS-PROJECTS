#!/usr/bin/env bash
# Local end-to-end demo with zero cloud dependencies (SQLite + mock tools).
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="${GOROOT:-/usr/local/go}/bin:$PATH"
rm -rf data
go build -o bin/agent ./cmd/agent
./bin/agent demo
echo
echo "Starting server on :8080 (GraphiQL at http://localhost:8080/graphql) — Ctrl+C to stop."
PORT=8080 POLL_SECONDS=10 ./bin/agent serve
