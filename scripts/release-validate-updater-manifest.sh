#!/usr/bin/env bash
# Validate the complete static updater-manifest contract.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FILE=""
EXPECTED_VERSION=""
# A published manifest is read to be replaced or to locate a prior release, so
# its packages need not be installable; a manifest about to publish must be.
EXISTING=0

usage() {
  echo "Usage: release-validate-updater-manifest.sh --file PATH [--version VERSION] [--existing]" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --file) FILE="${2:-}"; shift 2 ;;
    --version) EXPECTED_VERSION="${2:-}"; shift 2 ;;
    --existing) EXISTING=1; shift ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "${FILE}" && -f "${FILE}" ]] || usage
: "${DOWNLOAD_BASE_URL:?missing DOWNLOAD_BASE_URL}"

VERSION="$(jq -er '.version | select(type == "string" and length > 0)' "${FILE}")" || {
  echo "error: updater manifest has no non-empty string .version" >&2
  exit 1
}
[[ "${VERSION}" != v* ]] || {
  echo "error: updater manifest version must not include a leading v" >&2
  exit 1
}
python3 "${ROOT}/scripts/semver-compare.py" eq "${VERSION}" "${VERSION}" >/dev/null

if [[ -n "${EXPECTED_VERSION}" ]]; then
  python3 "${ROOT}/scripts/semver-compare.py" eq "${EXPECTED_VERSION}" "${EXPECTED_VERSION}" >/dev/null
  [[ "${VERSION}" == "${EXPECTED_VERSION}" ]] || {
    echo "error: updater manifest version ${VERSION} does not equal expected ${EXPECTED_VERSION}" >&2
    exit 1
  }
fi

python3 - "${FILE}" "${ROOT}/packaging/release-platforms.json" "${DOWNLOAD_BASE_URL%/}" "${ROOT}/scripts" "${EXISTING}" <<'PY'
import datetime
import json
import re
import sys

sys.path.insert(0, sys.argv[4])
from update_keys import load_registry, validate_binding
from updater_signature import require_version

with open(sys.argv[1], encoding="utf-8") as handle:
    manifest = json.load(handle)
with open(sys.argv[2], encoding="utf-8") as handle:
    catalog = json.load(handle)
if set(manifest) - {"withdrawn"} != {"notes", "platforms", "pub_date", "version", "update_keys"}:
    raise SystemExit("error: updater manifest has unsupported top-level fields")
if "withdrawn" in manifest and manifest["withdrawn"] is not True:
    raise SystemExit("error: withdrawn marker must be true when present")
validate_binding(load_registry(), manifest)
if not isinstance(manifest["notes"], str) or not manifest["notes"].strip():
    raise SystemExit("error: updater manifest notes are empty")
rows = catalog.get("platforms")
if catalog.get("schema_version") != 1 or not isinstance(rows, list) or not rows:
    raise SystemExit("error: release platform catalog has an unsupported shape")
all_platforms = {row["updater_key"]: row for row in rows}
if len(all_platforms) != len(rows):
    raise SystemExit("error: release platform catalog repeats an updater key")
expected = {
    key: row for key, row in all_platforms.items()
    if row.get("publication") == "public"
}
if not expected:
    raise SystemExit("error: release platform catalog has no public updater platform")
if set(manifest["platforms"]) != set(expected):
    raise SystemExit(
        "error: updater manifest platforms do not equal the public platform catalog"
    )
version = manifest["version"]
for key, row in expected.items():
    extension = row.get("updater_extension")
    if not re.fullmatch(r"[a-z0-9]+-[a-z0-9_]+", key) or not isinstance(extension, str) or not re.fullmatch(r"[A-Za-z0-9.]+", extension):
        raise SystemExit(f"error: release platform catalog entry {key!r} is invalid")
    value = manifest["platforms"][key]
    if set(value) != {"signature", "url"}:
        raise SystemExit(f"error: updater entry {key} has an unsupported shape")
    if not isinstance(value["signature"], str) or not value["signature"].strip():
        raise SystemExit(f"error: updater entry {key} has an empty signature")
    if sys.argv[5] != "1":
        try:
            require_version(value["signature"], version, f"updater entry {key}")
        except ValueError as exc:
            raise SystemExit(f"error: {exc}")
    wanted = (
        f"{sys.argv[3]}/releases/v{version}/painted-wolf-code_v{version}_"
        f"{key}.{extension}"
    )
    if value["url"] != wanted:
        raise SystemExit(f"error: updater entry {key} URL does not equal {wanted}")
value = manifest["pub_date"]
try:
    parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timezone offset is required")
except (TypeError, ValueError) as exc:
    raise SystemExit(f"error: updater manifest pub_date is not RFC 3339: {exc}")
PY
