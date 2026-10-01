#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"
BIN_DIR="${PW_BIN_DIR}"
GO_DIR="${ROOT}/lycaon"
DEN_DIR="${ROOT}/lycaon-den"
TAURI_DIR="${DEN_DIR}/src-tauri"
OUT="${ROOT}/THIRD-PARTY-NOTICES.md"
TAURI_COPY="${TAURI_DIR}/THIRD-PARTY-NOTICES.md"

GO_LICENSES_VERSION="v2.0.1"
CARGO_ABOUT_VERSION="0.9.1"

GO_LICENSES="${BIN_DIR}/go-licenses"
CARGO_ABOUT="${BIN_DIR}/cargo-about"

export GOTOOLCHAIN="go$(grep '^go ' "${GO_DIR}/go.mod" | awk '{print $2}')"
GOROOT="$(go env GOROOT)"
export GOROOT
mkdir -p "${BIN_DIR}"

install_go_licenses() {
  echo "licenses:notices — installing go-licenses ${GO_LICENSES_VERSION}" >&2
  (cd "${GO_DIR}" && GOBIN="${BIN_DIR}" go install "github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}")
}

install_cargo_about() {
  echo "licenses:notices — installing cargo-about ${CARGO_ABOUT_VERSION}" >&2
  # The installer places the executable one directory below the requested root.
  cargo install cargo-about \
    --version "${CARGO_ABOUT_VERSION}" \
    --locked \
    --features cli \
    --root "${BIN_DIR}/cargo-about-root"
  mv -f "${BIN_DIR}/cargo-about-root/bin/cargo-about" "${CARGO_ABOUT}"
  rm -rf "${BIN_DIR}/cargo-about-root"
}

if [[ ! -x "${GO_LICENSES}" ]]; then
  install_go_licenses
fi
if [[ ! -x "${CARGO_ABOUT}" ]]; then
  install_cargo_about
fi

if ! command -v bun >/dev/null 2>&1; then
  echo "error: bun required for licenses:notices" >&2
  exit 1
fi
if [[ ! -d "${DEN_DIR}/node_modules" ]]; then
  echo "error: ${DEN_DIR}/node_modules missing — run bun install in lycaon-den" >&2
  exit 1
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/licenses-notices.XXXXXX")"
cleanup() { rm -rf "${TMP}"; }
trap cleanup EXIT

echo "licenses:notices — Go modules (go-licenses)" >&2
# Unclassified licenses are attributed through licensing/go-license-overrides.yaml.
GO_IGNORE_FIRST_PARTY=(--ignore=github.com/lycaon/lycaon)
GO_IGNORE_UNCLASSIFIED=(
  # Classifier misses the module-root Apache-2.0.
  --ignore=github.com/deitch/magic
  # The embedded font uses OFL-1.1, which the classifier does not recognize.
  --ignore=github.com/alecthomas/chroma/v2
)
# Dual-license exemptions affect checks; their license texts remain in the output.
GO_IGNORE_DUAL_LICENSED=(
  # Apache-2.0 is the selected option from the shared dual-license file.
  --ignore=github.com/spdx/tools-golang
)
# This package list matches the staged executables.
SHIPPED_GO_PACKAGES=(./cmd/lycaon ./cmd/pw-logs)
(
  cd "${GO_DIR}"
  "${GO_LICENSES}" check "${SHIPPED_GO_PACKAGES[@]}" \
    "${GO_IGNORE_FIRST_PARTY[@]}" \
    "${GO_IGNORE_UNCLASSIFIED[@]}" \
    "${GO_IGNORE_DUAL_LICENSED[@]}" \
    --disallowed_types=forbidden,restricted,unknown
  # Warnings/classifier misses go to stderr; CSV rows (including Unknown) to stdout.
  "${GO_LICENSES}" csv "${SHIPPED_GO_PACKAGES[@]}" \
    "${GO_IGNORE_FIRST_PARTY[@]}" \
    >"${TMP}/go.csv" 2>"${TMP}/go-csv.err"
  if [[ ! -s "${TMP}/go.csv" ]]; then
    echo "error: go-licenses csv produced no output" >&2
    cat "${TMP}/go-csv.err" >&2 || true
    exit 1
  fi
  "${GO_LICENSES}" save "${SHIPPED_GO_PACKAGES[@]}" \
    "${GO_IGNORE_FIRST_PARTY[@]}" \
    "${GO_IGNORE_UNCLASSIFIED[@]}" \
    --save_path="${TMP}/go-save" \
    --force
)

# The fixture requires assembly to reject an unknown license.
if [[ "${LICENSES_NOTICES_FIXTURE:-}" == "1" ]]; then
  echo "github.com/example/unlicensed-fixture,Unknown,Unknown" >>"${TMP}/go.csv"
fi

echo "licenses:notices — Den npm production graph" >&2
bun "${ROOT}/scripts/licenses-npm-notices.ts" --out "${TMP}/npm.md"

echo "licenses:notices — Rust crates (cargo-about)" >&2
(
  cd "${TAURI_DIR}"
  "${CARGO_ABOUT}" generate --fail --format json -o "${TMP}/crates.json"
)

echo "licenses:notices — embedded document core crates" >&2
(
  cd "${GO_DIR}/internal/documentcore/native"
  "${CARGO_ABOUT}" generate --fail --format json --config "${TAURI_DIR}/about.toml" -o "${TMP}/document-crates.json"
)

echo "licenses:notices — decision engine crates" >&2
# Apple silicon ships the widest feature set. MLX's C++ is outside cargo's view;
# bundled-binaries.yaml carries it.
DECIDE_FEATURES="$(BIALY_FEATURES="" bash "${ROOT}/scripts/decide-features.sh" Darwin/arm64)"
if [[ ",${DECIDE_FEATURES}," != *",mlx,"* ]]; then
  echo "error: decision engine notice features (${DECIDE_FEATURES:-none}) omit mlx" >&2
  exit 1
fi
(
  cd "${GO_DIR}/internal/decide/native"
  "${CARGO_ABOUT}" generate --fail --format json --config "${TAURI_DIR}/about.toml" \
    --features "${DECIDE_FEATURES}" -o "${TMP}/decide-crates.json"
)

echo "licenses:notices — verified Opengrep release notices" >&2
OPENGREP_ARTIFACT="$("${ROOT}/scripts/resolve-opengrep.sh" --artifact-dir-only)"

echo "licenses:notices — assemble" >&2
set +e
bun "${ROOT}/scripts/licenses-assemble.ts" \
  --go-csv "${TMP}/go.csv" \
  --go-save "${TMP}/go-save" \
  --npm-md "${TMP}/npm.md" \
  --crates-json "${TMP}/crates.json" \
  --document-crates-json "${TMP}/document-crates.json" \
  --decide-crates-json "${TMP}/decide-crates.json" \
  --opengrep-artifact "${OPENGREP_ARTIFACT}" \
  --out "${OUT}"
assemble_rc=$?
set -e

if [[ "${LICENSES_NOTICES_FIXTURE:-}" == "1" ]]; then
  if [[ "${assemble_rc}" -eq 0 ]]; then
    echo "error: fail-closed fixture expected assemble to fail" >&2
    exit 1
  fi
  echo "licenses:notices — fail-closed fixture OK (assemble rejected Unknown)" >&2
  exit 0
fi

if [[ "${assemble_rc}" -ne 0 ]]; then
  exit "${assemble_rc}"
fi

cp -f "${OUT}" "${TAURI_COPY}"
echo "licenses:notices — wrote ${OUT}" >&2
echo "licenses:notices — copied ${TAURI_COPY} (Tauri resources)" >&2
