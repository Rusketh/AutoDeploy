#!/usr/bin/env bash
# scripts/backup.sh
#
# Snapshot AutoDeploy's persistent state into a single tar.gz.
#
# What's included:
#   - SQLite database (consistent snapshot via .backup)
#   - At-rest secrets key (data/secrets-key.bin)
#   - Bootstrap-admin file if present
#   - Branding assets (system_setting JSON is already in the DB)
#   - TLS material if present
#
# What's NOT included:
#   - Payload blobs (iso/, drivers/, software/, ipxe/). These are large
#     and are typically re-uploadable from the operator's source-of-truth
#     installer library. Take separate snapshots of $AUTODEPLOY_DATA_DIR
#     if you need them in the same archive.
#
# Usage:
#   AUTODEPLOY_DATA_DIR=/var/lib/autodeploy scripts/backup.sh /backups
set -euo pipefail

DATA_DIR="${AUTODEPLOY_DATA_DIR:-./data}"
OUT_DIR="${1:-./backups}"

mkdir -p "$OUT_DIR"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Consistent SQLite snapshot.
if command -v sqlite3 >/dev/null 2>&1 && [ -f "$DATA_DIR/autodeploy.sqlite" ]; then
    sqlite3 "$DATA_DIR/autodeploy.sqlite" ".backup '$work/autodeploy.sqlite'"
fi

# Secrets key and TLS material.
[ -f "$DATA_DIR/secrets-key.bin" ] && cp -a "$DATA_DIR/secrets-key.bin" "$work/"
[ -d "$DATA_DIR/tls" ] && cp -a "$DATA_DIR/tls" "$work/"
[ -f "$DATA_DIR/admin-bootstrap.txt" ] && cp -a "$DATA_DIR/admin-bootstrap.txt" "$work/"

out="$OUT_DIR/autodeploy-$stamp.tar.gz"
tar -C "$work" -czf "$out" .
echo "wrote $out"

# Permissions: 0600 because the archive contains the secrets-key, which
# decrypts the domain-join and webhook secrets stored in the database.
chmod 0600 "$out"
