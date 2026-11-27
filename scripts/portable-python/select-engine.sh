#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MANIFEST="${ROOT_DIR}/lycaon/config/runtime/portable-python/bundled-manifest.yaml"

DESCRIPTOR=""
TAG=""
EXPECTED_COMMIT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --descriptor)
      DESCRIPTOR="${2:-}"
      shift 2
      ;;
    --tag)
      TAG="${2:-}"
      shift 2
      ;;
    --expected-commit)
      EXPECTED_COMMIT="${2:-}"
      shift 2
      ;;
    --manifest)
      MANIFEST="${2:-}"
      shift 2
      ;;
    *)
      echo "usage: $0 --descriptor <path-or-https-url> --tag <release-tag> --expected-commit <40-hex-sha> [--manifest <path>]" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${DESCRIPTOR}" || -z "${TAG}" || -z "${EXPECTED_COMMIT}" ]]; then
  echo "usage: $0 --descriptor <path-or-https-url> --tag <release-tag> --expected-commit <40-hex-sha> [--manifest <path>]" >&2
  exit 2
fi

(
  cd "${ROOT_DIR}/lycaon"
  go run ./cmd/portable-python-artifact \
    -mode select \
    -manifest "${MANIFEST}" \
    -descriptor "${DESCRIPTOR}" \
    -tag "${TAG}" \
    -expected-commit "${EXPECTED_COMMIT}"
)
