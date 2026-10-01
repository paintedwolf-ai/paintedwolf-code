#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION_FILE="${ROOT}/VERSION"
if [[ ! -f "${VERSION_FILE}" ]]; then
  echo "error: missing ${VERSION_FILE}" >&2
  exit 1
fi
eval "$(python3 "${ROOT}/scripts/release-metadata.py" --root "${ROOT}" --format shell)"

PKG="${ROOT}/lycaon-den/package.json"
TAURI="${ROOT}/lycaon-den/src-tauri/tauri.conf.json"
CARGO="${ROOT}/lycaon-den/src-tauri/Cargo.toml"

python3 - "${PRODUCT_VERSION}" "${NATIVE_VERSION}" "${MACOS_BUNDLE_VERSION}" "${PKG}" "${TAURI}" "${ROOT}/scripts" <<'PY'
import json, sys
product, native, build, package_path, tauri_path, scripts = sys.argv[1:]
sys.path.insert(0, scripts)
from update_keys import load_registry, generation, feed_key
keys = load_registry()
embedded = keys["embedded_generation"]
with open(package_path, encoding="utf-8") as f:
    package = json.load(f)
package["version"] = product
with open(package_path, "w", encoding="utf-8") as f:
    json.dump(package, f, indent=2, ensure_ascii=False)
    f.write("\n")
with open(tauri_path, encoding="utf-8") as f:
    tauri = json.load(f)
tauri["plugins"]["updater"]["pubkey"] = generation(keys, embedded)["public_key"]
tauri["plugins"]["updater"]["endpoints"] = ["https://downloads.paintedwolf.dev/" + feed_key("stable", embedded)]
tauri["version"] = native
tauri["bundle"]["macOS"]["bundleVersion"] = build
with open(tauri_path, "w", encoding="utf-8") as f:
    json.dump(tauri, f, indent=2, ensure_ascii=False)
    f.write("\n")
PY

python3 - "${NATIVE_VERSION}" "${CARGO}" <<'PY'
import re, sys
version, path = sys.argv[1], sys.argv[2]
text = open(path, encoding="utf-8").read()
new, n = re.subn(
    r'(?m)^(version\s*=\s*")[^"]*(")',
    rf"\g<1>{version}\2",
    text,
    count=1,
)
if n != 1:
    raise SystemExit(f"expected one package version= in {path}, got {n}")
open(path, "w", encoding="utf-8").write(new)
PY

echo "synced den product ${PRODUCT_VERSION} → native ${NATIVE_VERSION} (${CHANNEL}, build ${RELEASE_BUILD})" >&2
