#!/usr/bin/env bash
# Resolve the pinned @redocly/cli binary. Verification stages run offline, so
# they need the lycaon-den install from ./task setup-dev; npx serves checkouts
# that have not installed it yet.
# shellcheck source=redocly-version.sh
source "$(dirname "${BASH_SOURCE[0]}")/redocly-version.sh"

REDOCLY_LOCAL_BIN="$ROOT/lycaon-den/node_modules/.bin/redocly"

redocly() {
  if [[ -x "$REDOCLY_LOCAL_BIN" ]]; then
    "$REDOCLY_LOCAL_BIN" "$@"
  else
    npx --yes "@redocly/cli@${REDOCLY_VERSION}" "$@"
  fi
}
