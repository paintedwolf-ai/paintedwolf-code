#!/usr/bin/env bash
# Fetch the pinned paintedwolf-tesseract release archive and verify it into
# engine-root/ocr.
#
#   scripts/ocrengine-fetch.sh [--target TRIPLE] [--archive FILE]
#
# --archive imports a local archive, such as a producer qualification build;
# it must match the pin exactly.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PIN="${ROOT}/lycaon/config/runtime/ocrengine/pin.yaml"
DEST="${OCRENGINE_DEST:-${ROOT}/lycaon-den/src-tauri/engine-root/ocr}"
TARGET=""
ARCHIVE="${OCRENGINE_ARCHIVE:-}"
TRAINED_DATA_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      [[ $# -ge 2 ]] || { echo "ocrengine-fetch: --target requires a platform triple" >&2; exit 2; }
      TARGET="$2"; shift ;;
    --trained-data-dir)
      [[ $# -ge 2 ]] || { echo "ocrengine-fetch: --trained-data-dir requires a directory" >&2; exit 2; }
      TRAINED_DATA_DIR="$2"; shift ;;
    --archive)
      [[ $# -ge 2 ]] || { echo "ocrengine-fetch: --archive requires a release archive" >&2; exit 2; }
      ARCHIVE="$2"; shift ;;
    *) echo "ocrengine-fetch: unsupported argument: $1" >&2; exit 2 ;;
  esac
  shift
done
if [[ -z "${TARGET}" ]]; then
  if command -v rustc >/dev/null 2>&1; then
    TARGET="$(rustc --print host-tuple)"
  else
    TARGET="$(uname -m)-$(uname -s)"
  fi
fi
case "${TARGET}" in
  x86_64-unknown-linux-gnu | x86_64-Linux) PLATFORM_KEY="linux-amd64" ;;
  aarch64-unknown-linux-gnu | aarch64-Linux) PLATFORM_KEY="linux-arm64" ;;
  *)
    echo "error: no bundled OCR engine for ${TARGET}; macOS recognizes text in-process" >&2
    exit 1
    ;;
esac

read_pin() {
  local key="$1"
  awk -v want="${key}" '
    $1 == want ":" {
      line = $0
      sub(/^[^:]+:[[:space:]]*/, "", line)
      gsub(/"/, "", line)
      print line
      exit
    }
  ' "${PIN}"
}

# Reads one key of a two-space indented section (a source or a platform).
read_section() {
  local section="$1" key="$2"
  awk -v section="${section}" -v want="${key}" '
    $0 ~ "^[[:space:]][[:space:]]" section ":" { in_section = 1; next }
    in_section && /^[[:space:]][[:space:]][a-z0-9_-]+:/ { exit }
    in_section && $1 == want ":" {
      line = $0
      sub(/^[^:]+:[[:space:]]*/, "", line)
      gsub(/"/, "", line)
      print line
      exit
    }
  ' "${PIN}"
}

# Reads one key of a top-level mapping such as producer.
read_mapping() {
  local mapping="$1" key="$2"
  awk -v mapping="${mapping}" -v want="${key}" '
    $0 == mapping ":" { in_mapping = 1; next }
    in_mapping && /^[^[:space:]]/ { exit }
    in_mapping && $1 == want ":" {
      line = $0
      sub(/^[^:]+:[[:space:]]*/, "", line)
      gsub(/"/, "", line)
      print line
      exit
    }
  ' "${PIN}"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print tolower($1)}'
  else
    shasum -a 256 "$1" | awk '{print tolower($1)}'
  fi
}

size_of() {
  stat -c %s "$1" 2>/dev/null || stat -f %z "$1"
}

ENGINE_VERSION="$(read_pin engine_version)"
URL_TMPL="$(read_section "${PLATFORM_KEY}" url)"
ARCHIVE_SHA="$(read_section "${PLATFORM_KEY}" sha256)"
ARCHIVE_BYTES="$(read_section "${PLATFORM_KEY}" bytes)"
BINARY_SHA="$(read_section "${PLATFORM_KEY}" binary_sha256)"
BINARY_BYTES="$(read_section "${PLATFORM_KEY}" binary_bytes)"
HAS_ENGLISH=0
HAS_ORIENTATION=0
DATA_LANGUAGES=()
DATA_SHAS=()
DATA_BYTES=()
DATA_SOURCES=()
while IFS= read -r key; do
  [[ "${key}" =~ ^([a-z]{3}(_[a-z]{3,4})?)_traineddata$ ]] || { echo "error: invalid trained-data key" >&2; exit 1; }
  language="${key%_traineddata}"
  for previous in "${DATA_LANGUAGES[@]}"; do
    [[ "${previous}" != "${language}" ]] || { echo "error: duplicate trained data" >&2; exit 1; }
  done
  [[ "${language}" != eng ]] || HAS_ENGLISH=1
  [[ "${language}" != osd ]] || HAS_ORIENTATION=1
  digest="$(read_section "${key}" sha256)"
  bytes="$(read_section "${key}" bytes)"
  [[ "${digest}" =~ ^[0-9a-f]{64}$ && "${bytes}" =~ ^[1-9][0-9]*$ ]] || { echo "error: unpinned trained data" >&2; exit 1; }
  source="$(read_section "${key}" source)"
  [[ -z "${source}" || "${source}" == tessdata-fast ]] || { echo "error: unknown trained-data source" >&2; exit 1; }
  if [[ "${language}" == eng || "${language}" == osd ]]; then
    [[ -z "${source}" ]] || { echo "error: base OCR models must come from the producer archive" >&2; exit 1; }
  fi
  DATA_SOURCES+=("${source}")
  DATA_LANGUAGES+=("${language}")
  DATA_SHAS+=("${digest}")
  DATA_BYTES+=("${bytes}")
done < <(awk '
  $0 == "trained_data:" { inside = 1; next }
  inside && /^[^[:space:]]/ { exit }
  inside && /^  [^[:space:]]+:/ { key = $1; sub(/:$/, "", key); print key }
' "${PIN}")
if [[ -z "${ENGINE_VERSION}" || -z "${URL_TMPL}" || "${HAS_ENGLISH}" != 1 || "${HAS_ORIENTATION}" != 1 || ${#DATA_LANGUAGES[@]} -lt 2 || ${#DATA_LANGUAGES[@]} -gt 9 ]]; then
  echo "error: incomplete or oversized OCR pin in ${PIN}" >&2
  exit 1
fi

if [[ -z "${ARCHIVE_SHA}" || "${ARCHIVE_BYTES}" == "0" || -z "${BINARY_SHA}" ]]; then
  echo "error: the ${PLATFORM_KEY} OCR engine is not published: ${PIN} carries no archive digest for it" >&2
  exit 1
fi
URL="${URL_TMPL//\{engine_version\}/${ENGINE_VERSION}}"
PRODUCER_REPOSITORY="$(read_mapping producer repository)"
PRODUCER_TAG="$(read_mapping producer tag)"
if [[ -z "${PRODUCER_REPOSITORY}" || "${PRODUCER_TAG}" != "v${ENGINE_VERSION}" || "${URL}" != "https://github.com/${PRODUCER_REPOSITORY}/releases/download/${PRODUCER_TAG}/"* ]]; then
  echo "error: ${PIN} must fetch ${PLATFORM_KEY} from the ${PRODUCER_REPOSITORY} release ${PRODUCER_TAG}" >&2
  exit 1
fi

# verify_payload checks one file's size and digest against the pin.
verify_payload() {
  local path="$1" want_sha="$2" want_bytes="$3"
  if [[ ! -f "${path}" ]]; then
    echo "error: staged OCR engine is missing ${path}" >&2
    return 1
  fi
  if [[ "$(size_of "${path}")" != "${want_bytes}" || "$(sha256_of "${path}")" != "${want_sha}" ]]; then
    echo "error: ${path} differs from the pinned payload" >&2
    return 1
  fi
}

verify_tree() {
  local root="$1"
  verify_payload "${root}/bin/tesseract" "${BINARY_SHA}" "${BINARY_BYTES}" || return 1
  local index
  for ((index=0; index<${#DATA_LANGUAGES[@]}; index++)); do
    verify_payload "${root}/tessdata/${DATA_LANGUAGES[index]}.traineddata" "${DATA_SHAS[index]}" "${DATA_BYTES[index]}" || return 1
  done
  [[ -x "${root}/bin/tesseract" ]]
}

if [[ -f "${DEST}/VERSION" && "$(tr -d '[:space:]' < "${DEST}/VERSION")" == "${ENGINE_VERSION}" ]] && verify_tree "${DEST}" 2>/dev/null; then
  echo "ocrengine-fetch — already at ${ENGINE_VERSION} (${DEST#${ROOT}/})" >&2
  exit 0
fi

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ocrengine-fetch.XXXXXX")"
cleanup() { rm -rf "${TMP_DIR}"; }
trap cleanup EXIT

if [[ -n "${ARCHIVE}" ]]; then
  case "${ARCHIVE}" in
    /*) ;;
    *) ARCHIVE="${PWD}/${ARCHIVE}" ;;
  esac
  echo "ocrengine-fetch — importing ${ARCHIVE}" >&2
  cp "${ARCHIVE}" "${TMP_DIR}/engine.tar.gz"
else
  echo "ocrengine-fetch — downloading ${URL}" >&2
  curl -fsSL -o "${TMP_DIR}/engine.tar.gz" "${URL}"
fi

ACTUAL_SHA="$(sha256_of "${TMP_DIR}/engine.tar.gz")"
ACTUAL_BYTES="$(size_of "${TMP_DIR}/engine.tar.gz")"
if [[ "${ACTUAL_SHA}" != "${ARCHIVE_SHA}" || "${ACTUAL_BYTES}" != "${ARCHIVE_BYTES}" ]]; then
  echo "error: OCR engine archive differs from the pin — deleting it" >&2
  echo "  got:  ${ACTUAL_SHA} (${ACTUAL_BYTES} bytes)" >&2
  echo "  want: ${ARCHIVE_SHA} (${ARCHIVE_BYTES} bytes)" >&2
  exit 1
fi

mkdir -p "${TMP_DIR}/extract"
tar -xzf "${TMP_DIR}/engine.tar.gz" -C "${TMP_DIR}/extract"
STAGE="${TMP_DIR}/extract/ocr"
if [[ ! -d "${STAGE}" ]]; then
  echo "error: archive does not contain an ocr/ payload" >&2
  exit 1
fi
if [[ "$(tr -d '[:space:]' < "${STAGE}/VERSION")" != "${ENGINE_VERSION}" ]]; then
  echo "error: archive VERSION $(cat "${STAGE}/VERSION") differs from the pin ${ENGINE_VERSION}" >&2
  exit 1
fi
MODEL_REPOSITORY="$(read_mapping trained_data_source repository)"
MODEL_COMMIT="$(read_mapping trained_data_source commit)"
for ((index=0; index<${#DATA_LANGUAGES[@]}; index++)); do
  [[ "${DATA_SOURCES[index]}" == tessdata-fast ]] || continue
  if [[ "${MODEL_REPOSITORY}" != tesseract-ocr/tessdata_fast || ! "${MODEL_COMMIT}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "error: supplemental OCR models require an immutable tessdata_fast source" >&2
    exit 1
  fi
  model="${DATA_LANGUAGES[index]}.traineddata"
  payload="${TMP_DIR}/${model}"
  if [[ -n "${TRAINED_DATA_DIR}" ]]; then
    cp "${TRAINED_DATA_DIR}/${model}" "${payload}"
  else
    curl -fsSL --max-filesize "${DATA_BYTES[index]}" -o "${payload}" \
      "https://raw.githubusercontent.com/${MODEL_REPOSITORY}/${MODEL_COMMIT}/${model}"
  fi
  verify_payload "${payload}" "${DATA_SHAS[index]}" "${DATA_BYTES[index]}"
  mv "${payload}" "${STAGE}/tessdata/${model}"
done
verify_tree "${STAGE}"
chmod -R u+rwX,go+rX "${STAGE}"

rm -rf "${DEST}"
mkdir -p "$(dirname "${DEST}")"
mv "${STAGE}" "${DEST}"
echo "ocrengine-fetch — staged ${DEST#${ROOT}/} (tesseract ${ENGINE_VERSION}, ${PLATFORM_KEY})" >&2
