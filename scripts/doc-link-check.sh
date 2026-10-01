#!/usr/bin/env bash
# Validate relative markdown links + heading anchors under docs/.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

cd "${ROOT}/lycaon"
go run ./cmd/doc-link-check -root "${ROOT}"
echo "doc links OK" >&2
