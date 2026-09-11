#!/bin/sh
# Consistent online backup of the history database using SQLite's backup API
# via the sqlite3 CLI (must be installed on the host), or an offline copy when
# the service is stopped. Never copies qB downloads. The master key is NOT
# included on purpose: store it separately.
set -eu
DATA_DIR="${HISTORY_DATA_DIR:-./data}"
OUT="${1:-./backups/history-$(date -u +%Y%m%dT%H%M%SZ).sqlite}"
mkdir -p "$(dirname "$OUT")"
if command -v sqlite3 >/dev/null 2>&1; then
  sqlite3 "$DATA_DIR/history.sqlite" ".backup '$OUT'"
  echo "online backup written to $OUT"
else
  echo "sqlite3 CLI not found; stopping the service for an offline copy (main + WAL are checkpointed on stop)"
  docker compose stop qbit-history
  cp "$DATA_DIR/history.sqlite" "$OUT"
  docker compose start qbit-history
  echo "offline backup written to $OUT"
fi
chmod 600 "$OUT"
