#!/bin/sh
# Local demo: mock qB + history server with a throw-away data directory.
# Usage: sh scripts/dev.sh   (then open http://127.0.0.1:28637, admin / local-demo-password)
set -eu
cd "$(dirname "$0")/.."
mkdir -p .local bin
go build -o bin/history ./cmd/history
go build -o bin/mockqb ./cmd/mockqb
if [ ! -f webassets/dist/index.html ]; then
  (cd web && npm ci && npm run build)
fi
./bin/mockqb -listen 127.0.0.1:18080 -torrents "${MOCK_TORRENTS:-40}" &
MOCK=$!
trap 'kill $MOCK 2>/dev/null || true' EXIT INT TERM
echo "mock qB: http://127.0.0.1:18080 (admin / adminadmin)"
HISTORY_ADMIN_PASSWORD="${HISTORY_ADMIN_PASSWORD:-local-demo-password}" ./bin/history -data .local/data -master-key .local/master.key -listen 127.0.0.1:28637 -web webassets/dist
