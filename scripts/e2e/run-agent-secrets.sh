#!/usr/bin/env bash
# End-to-end test of agent secrets and the upgrade path, with real binaries.
#
# Builds the CURRENT RELEASE server and agent from a git ref (default
# origin/main) and the server and agent from this checkout, then runs:
#   agent_secrets_upgrade.py - release site -> server upgrade -> agent
#                              self-update -> rotation -> deploy issuance ->
#                              takeover attempt + approval -> approve all ->
#                              clones -> media enrolment
#   agent_secrets_compat.py  - new agent against the release server
#
# Usage: scripts/e2e/run-agent-secrets.sh [release-ref]
# Needs: go, git, python3. Uses 127.0.0.1:18090 and a temp directory.
# Set E2E_SHOTS=<dir> to also save portal screenshots there (needs node and
# Playwright).
# The agents are stamped with versions (release v1.0.0, this checkout v9.9.9)
# so the release agent self-updates to the new one, as a real site's would.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
REF="${1:-origin/main}"
TMP="$(mktemp -d)"
trap 'git -C "$ROOT" worktree remove --force "$TMP/release" >/dev/null 2>&1 || true; rm -rf "$TMP"' EXIT
export E2E_BIN="$TMP/bin" E2E_RUN="$TMP/run"
mkdir -p "$E2E_BIN"

git -C "$ROOT" worktree add -q "$TMP/release" "$REF"
(cd "$TMP/release/server" && CGO_ENABLED=0 go build -o "$E2E_BIN/server-old" ./cmd/autodeploy-server)
(cd "$TMP/release/agent"  && CGO_ENABLED=0 go build -ldflags "-X main.Version=v1.0.0" -o "$E2E_BIN/agent-old" ./cmd/autodeploy-agent)
(cd "$ROOT/server" && CGO_ENABLED=0 go build -o "$E2E_BIN/server-new" ./cmd/autodeploy-server)
(cd "$ROOT/agent"  && CGO_ENABLED=0 go build -ldflags "-X main.Version=v9.9.9" -o "$E2E_BIN/agent-new" ./cmd/autodeploy-agent)
if [ -n "${E2E_SHOTS:-}" ]; then mkdir -p "$E2E_SHOTS"; E2E_SHOTS="$(cd "$E2E_SHOTS" && pwd)"; export E2E_SHOTS; fi

for t in agent_secrets_upgrade agent_secrets_compat; do
  rm -rf "$E2E_RUN" && mkdir -p "$E2E_RUN"
  echo "### $t"
  PYTHONDONTWRITEBYTECODE=1 python3 "$ROOT/scripts/e2e/$t.py"
done
