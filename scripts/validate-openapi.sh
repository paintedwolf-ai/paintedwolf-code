#!/usr/bin/env bash
# Validate docs/openapi.yaml with Redocly CLI.
# Called by Task (openapi:lint); may also be run directly.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# shellcheck source=redocly-cli.sh
source "$(dirname "$0")/redocly-cli.sh"

ENTRY="${OPENAPI_LINT_ENTRY:-docs/openapi/root.yaml}"
if [[ ! -f "$ENTRY" ]]; then
  ENTRY="docs/openapi.yaml"
fi
redocly lint "$ENTRY" --config docs/redocly.yaml
