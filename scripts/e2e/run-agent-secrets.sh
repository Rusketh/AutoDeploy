#!/usr/bin/env bash
# End-to-end test of agent secrets and the upgrade path, with real binaries.
#
# Builds the CURRENT RELEASE server and agent from a git ref (default
# origin/main) and the server and agent from this checkout, then runs:
#   agent_secrets_upgrade.py - release site -> server upgrade -> agent
#                              upgrade -> rotation -> deploy issuance ->
#                              takeover attempt + approval
#   agent_secrets_compat.py  - new agent against the release server
#
# Usage: scripts/e2e/run-agent-secrets.sh [release-ref]
# Needs: go, git, python3. Uses 127.0.0.1:18090 and a temp directory.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
REF="${1:-origin/main}"
TMP="$(mktemp -d)"
trap 'git -C "$ROOT" worktree remove --force "$TMP/release" >/dev/null 2>&1 || true; rm -rf "$TMP"' EXIT
export E2E_BIN="$TMP/bin" E2E_RUN="$TMP/run"
mkdir -p "$E2E_BIN"

git -C "$ROOT" worktree add -q "$TMP/release" "$REF"
(cd "$TMP/release/server" && CGO_ENABLED=0 go build -o "$E2E_BIN/server-old" ./cmd/autodeploy-server)
(cd "$TMP/release/agent"  && CGO_ENABLED=0 go build -o "$E2E_BIN/agent-old"  ./cmd/autodeploy-agent)
(cd "$ROOT/server" && CGO_ENABLED=0 go build -o "$E2E_BIN/server-new" ./cmd/autodeploy-server)
(cd "$ROOT/agent"  && CGO_ENABLED=0 go build -o "$E2E_BIN/agent-new"  ./cmd/autodeploy-agent)

for t in agent_secrets_upgrade agent_secrets_compat; do
  rm -rf "$E2E_RUN" && mkdir -p "$E2E_RUN"
  echo "### $t"
  python3 "$ROOT/scripts/e2e/$t.py"
done
