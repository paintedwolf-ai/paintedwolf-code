#!/usr/bin/env bash
# Prepare unsigned release compiler caches through setup-dev admission.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"
bin="$(python3 scripts/artifact_paths.py bin "${ROOT}")"
mkdir -p "${bin}"
if [[ ! -x "${bin}/cargo-about" ]]; then
  version="$(sed -n 's/^CARGO_ABOUT_VERSION="\(.*\)"/\1/p' scripts/licenses-notices.sh)"
  cargo install cargo-about --version "${version}" --locked --features cli --root "${RUNNER_TEMP}/cargo-about-root"
  mv -f "${RUNNER_TEMP}/cargo-about-root/bin/cargo-about" "${bin}/cargo-about"
fi
if [[ ! -x "${bin}/go-licenses" ]]; then
  version="$(sed -n 's/^GO_LICENSES_VERSION="\(.*\)"/\1/p' scripts/licenses-notices.sh)"
  (cd lycaon && GOBIN="${bin}" go install "github.com/google/go-licenses/v2@${version}")
fi

export MACOSX_DEPLOYMENT_TARGET="$(tr -d '[:space:]' < lycaon/internal/platformfloor/macos_floor.txt)"
export CGO_ENABLED=1
(cd lycaon && go build -trimpath -tags=paintedwolf_release -o "${RUNNER_TEMP}/pw" ./cmd/lycaon)
(cd lycaon && go build -trimpath -tags=paintedwolf_release -o "${RUNNER_TEMP}/pw-logs" ./cmd/pw-logs)

set -euo pipefail
bash scripts/sync-den-versions.sh
# Cache warming compiles the shell without shipping it; an empty notices resource satisfies the manifest.
[[ -f lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md ]] || : > lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md
bash scripts/ensure-tauri-binaries.sh
mkdir -p lycaon-den/src-tauri/engine-root
cd lycaon-den
bun install --frozen-lockfile
CI=false bun run tauri build --no-bundle --target "$(rustc --print host-tuple)"
