#!/usr/bin/env bash
# Populate pinned compiler/generator inputs before the offline lane starts.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/artifact-paths.sh"
source "$ROOT/scripts/sqlc-version.sh"
(cd "$ROOT/lycaon" && go mod download)
(cd "$ROOT/scripts/commentlint" && go mod download)
GOBIN="$PW_BIN_DIR" go install "github.com/sqlc-dev/sqlc/cmd/sqlc@v${SQLC_VERSION}"
for manifest in "$ROOT/lycaon/internal/documentcore/native/Cargo.toml" "$ROOT/lycaon/internal/decide/native/Cargo.toml"; do
  cargo fetch --locked --manifest-path "$manifest"
done
