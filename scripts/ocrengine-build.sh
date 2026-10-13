#!/usr/bin/env bash
# Build the static OCR engine archives for linux/amd64 and linux/arm64 from the
# sources pinned in lycaon/config/ocrengine/pin.yaml. Output: one archive, its
# digest, and its provenance per platform under the output directory.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
PIN="${ROOT}/lycaon/config/ocrengine/pin.yaml"
OUT="${OCRENGINE_BUILD_DIR:-${PW_BUILD_DIR}/ocrengine}"
PLATFORMS="${OCRENGINE_PLATFORMS:-amd64 arm64}"

if ! command -v docker >/dev/null 2>&1; then
  echo "error: docker with buildx is required to build the OCR engine" >&2
  exit 1
fi

# The pin schema is flat at the top level.
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

read_source() {
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

ENGINE_VERSION="$(read_pin engine_version)"
BASE_IMAGE="$(read_pin build_image)"
ARGS=(
  --build-arg "BASE_IMAGE=${BASE_IMAGE}"
  --build-arg "ENGINE_VERSION=${ENGINE_VERSION}"
  --build-arg "TESSERACT_VERSION=$(read_source tesseract version)"
  --build-arg "TESSERACT_SHA256=$(read_source tesseract sha256)"
  --build-arg "LEPTONICA_VERSION=$(read_source leptonica version)"
  --build-arg "LEPTONICA_SHA256=$(read_source leptonica sha256)"
  --build-arg "TESSDATA_FAST_COMMIT=$(read_source tessdata_fast commit)"
  --build-arg "ENG_SHA256=$(read_source eng_traineddata sha256)"
  --build-arg "OSD_SHA256=$(read_source osd_traineddata sha256)"
  --build-arg "MUSL_VERSION=$(read_source musl version)"
  --build-arg "MUSL_COPYRIGHT_SHA256=$(read_source musl copyright_sha256)"
  --build-arg "GCC_VERSION=$(read_source gcc_runtime version)"
  --build-arg "GPL3_SHA256=$(read_source gcc_runtime gpl3_sha256)"
  --build-arg "GCC_RUNTIME_EXCEPTION_SHA256=$(read_source gcc_runtime exception_sha256)"
  --build-arg "SOURCE_DATE_EPOCH=$(read_pin source_date_epoch)"
)
for arg in "${ARGS[@]}"; do
  if [[ "${arg}" == *= ]]; then
    echo "error: incomplete pin in ${PIN}: ${arg#--build-arg }" >&2
    exit 1
  fi
done

mkdir -p "${OUT}"
for arch in ${PLATFORMS}; do
  echo "ocrengine:build — linux/${arch} (${ENGINE_VERSION})" >&2
  docker buildx build --platform "linux/${arch}" --target artifact \
    --output "type=local,dest=${OUT}/linux-${arch}" \
    "${ARGS[@]}" \
    -f "${ROOT}/scripts/ocrengine/Dockerfile" "${ROOT}/scripts/ocrengine"
done

echo "ocrengine:build — archives under ${OUT}" >&2
for arch in ${PLATFORMS}; do
  cat "${OUT}/linux-${arch}"/*.tar.gz.sha256
  stat -c '%s %n' "${OUT}/linux-${arch}"/*.tar.gz 2>/dev/null || stat -f '%z %N' "${OUT}/linux-${arch}"/*.tar.gz
done
