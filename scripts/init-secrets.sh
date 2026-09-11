#!/bin/sh
# Creates the data directory and the credential master key for the container
# user (uid 65532). Run once on the VPS from the project directory before
# `docker compose up -d`. Safe to re-run: never overwrites an existing key.
set -eu
DATA_DIR="${HISTORY_DATA_DIR:-./data}"
KEY_FILE="${HISTORY_MASTER_KEY_FILE:-./secrets/master.key}"
mkdir -p "$DATA_DIR" "$(dirname "$KEY_FILE")"
if [ -e "$KEY_FILE" ]; then
  echo "master key already exists at $KEY_FILE (kept)"
else
  umask 077
  head -c 32 /dev/urandom > "$KEY_FILE"
  echo "generated master key at $KEY_FILE"
fi
chmod 600 "$KEY_FILE"
chmod 700 "$DATA_DIR" "$(dirname "$KEY_FILE")"
if [ "$(id -u)" = "0" ]; then
  chown 65532:65532 "$KEY_FILE" "$DATA_DIR" "$(dirname "$KEY_FILE")"
else
  echo "not root: make sure uid 65532 can read $KEY_FILE and write $DATA_DIR (e.g. sudo chown 65532:65532 ...)"
fi
echo "Back up $KEY_FILE separately from $DATA_DIR: without it, saved qB passwords cannot be decrypted."
