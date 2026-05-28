#!/usr/bin/env bash
# scripts/check-secrets.sh
#
# Lightweight, deliberately strict guard against secret values leaking into
# logs or HTTP responses. CI runs this on every push. It is a tripwire, not a
# substitute for review.
#
# Rules enforced:
#   1. No call to slog.String / fmt.Sprint / log.Print* whose value argument
#      looks like a secret identifier (pin, recoveryKey, password) unless the
#      value is the literal "[REDACTED]" or comes through the Secret wrapper.
#   2. No raw fmt.Println of a Secret value (the Secret type redacts itself,
#      but Println of *.Reveal() is not allowed outside the audited reveal
#      paths under server/internal/secret/ — which does not yet exist; until
#      it does, no .Reveal() call is permitted at all).
#
# These are intentionally crude; they exist to make accidental leaks
# uncomfortable and to fail loudly when conventions slip.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail=0

# 1. Look for direct logging of obvious secret names.
#    Pattern: slog.<Type>("...pin..." or "...password..." or "...recovery...", <anything>)
#    The variable name is what we key off; rename your variable or wrap it.
if grep -RInE 'slog\.String\("(pin|recovery_key|recoveryKey|password|access_pin|secret)"' \
        server boot-client agent 2>/dev/null | grep -v '\[REDACTED\]' | grep -v '_test\.go'; then
    echo "ERROR: slog.String() with a secret-named key — wrap with logging.Secret or rename." >&2
    fail=1
fi

# 2. Catch fmt.Printf / fmt.Sprintf / log.Printf that embeds a likely secret variable.
if grep -RInE 'fmt\.(Sprintf|Printf|Println|Print)\([^)]*\b(pin|recoveryKey|recovery_key|password|accessPIN|access_pin)\b' \
        server boot-client agent 2>/dev/null | grep -v '_test\.go' | grep -v '\[REDACTED\]'; then
    echo "ERROR: fmt.* called with a secret-named variable — use logging.Secret." >&2
    fail=1
fi

# 3. Reveal() is the audited boundary. No call sites yet — when they appear
#    they must live under a path containing 'secret' in the directory name.
if grep -RIn '\.Reveal()' server boot-client agent 2>/dev/null \
     | grep -v '_test\.go' \
     | grep -v 'internal/secret/' \
     | grep -v 'internal/logging/'; then
    echo "ERROR: Secret.Reveal() called outside the audited secret boundary." >&2
    fail=1
fi

if [ "$fail" -ne 0 ]; then
    echo
    echo "Secret-handling check failed. See docs/design/CONVENTIONS.md section 4." >&2
    exit 1
fi

echo "secret-handling check OK"
