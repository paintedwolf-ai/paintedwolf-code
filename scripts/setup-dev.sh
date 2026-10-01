#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

if [[ "${1:-}" == "--frontend" ]]; then
  (cd "${ROOT}/lycaon-den" && bun install --frozen-lockfile)
  exit 0
fi

if [[ "${1:-}" == "--documents" ]]; then
  bash "${ROOT}/scripts/build-document-core.sh" --setup
  (cd "${ROOT}/lycaon" && go mod download github.com/tetratelabs/wazero)
  (cd "${ROOT}/lycaon-den" && bun install)
  exit 0
fi

version_at_least() {
	local got="${1#v}" want="${2#v}" i
	got="${got%%[-+]*}"
	want="${want%%[-+]*}"
	local -a got_parts want_parts
	IFS=. read -r -a got_parts <<<"${got}"
	IFS=. read -r -a want_parts <<<"${want}"
	for i in 0 1 2; do
		local gv="${got_parts[$i]:-0}" wv="${want_parts[$i]:-0}"
		(( 10#${gv} > 10#${wv} )) && return 0
		(( 10#${gv} < 10#${wv} )) && return 1
	done
	return 0
}

require_tool() {
	local name="$1" minimum="$2" install="$3" version
	if ! command -v "${name}" >/dev/null 2>&1; then
		echo "error: ${name} ${minimum}+ is required — ${install}" >&2
		exit 1
	fi
	case "${name}" in
		go) version="$(go env GOVERSION)"; version="${version#go}" ;;
		node) version="$(node --version)" ;;
		rustc) version="$(rustc --version | awk '{print $2}')" ;;
		*) version="$(${name} --version | awk 'NR == 1 {print $1}')" ;;
	esac
	if ! version_at_least "${version}" "${minimum}"; then
		echo "error: ${name} ${version} is too old; need ${minimum}+ — ${install}" >&2
		exit 1
	fi
}

validate_toolchains() {
	echo "==> Validating pinned toolchains"
	if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
		echo "error: development setup supports macOS on Apple Silicon" >&2
		exit 1
	fi
	local macos_version macos_floor
	macos_version="$(sw_vers -productVersion)"
	macos_floor="$(tr -d '[:space:]' < "${ROOT}/lycaon/internal/platformfloor/macos_floor.txt")"
	if ! version_at_least "${macos_version}" "${macos_floor}"; then
		echo "error: macOS ${macos_version} is unsupported; need macOS ${macos_floor}+" >&2
		exit 1
	fi
	if ! command -v xcrun >/dev/null 2>&1 || ! xcrun --find clang >/dev/null 2>&1; then
		echo "error: Xcode Command Line Tools are required — run: xcode-select --install" >&2
		exit 1
	fi
	require_tool go "$(awk '/^go /{print $2}' "${ROOT}/lycaon/go.mod")" "https://go.dev/dl/"
	require_tool bun "$(tr -d '[:space:]' < "${ROOT}/.bun-version")" "https://bun.sh"
	require_tool node "$(tr -d '[:space:]' < "${ROOT}/.node-version")" "https://nodejs.org/"
	require_tool rustc "$(sed -n 's/^channel = "\(.*\)"/\1/p' "${ROOT}/rust-toolchain.toml")" "https://rustup.rs"
	command -v cargo >/dev/null 2>&1 || {
		echo "error: cargo is required with Rust — https://rustup.rs" >&2
		exit 1
	}
}

# shellcheck source=artifact-paths.sh
source "$(dirname "$0")/artifact-paths.sh"

bootstrap_golangci_lint() {
  bash "${ROOT}/scripts/lint-go.sh" fast >/dev/null 2>&1 || bash "${ROOT}/scripts/lint-go.sh" fast
}

validate_toolchains

bootstrap_golangci_lint

echo "==> Installing Den dependencies"
(
	cd "${ROOT}/lycaon-den"
	bun install --frozen-lockfile
)

echo "==> Provisioning managed development browser"
# The sidecar retries provisioning at launch.
if ! "${ROOT}/task" browser:ensure; then
  echo "warning: browser provisioning failed — it will retry when you run ./task den:sidecar; or retry with ./task browser:ensure" >&2
fi

if command -v direnv >/dev/null 2>&1; then
	echo "==> Allowing .envrc for optional bare task usage"
	direnv allow "${ROOT}"
else
	echo "==> direnv not installed; use ./task from the repository root"
fi

echo "==> Fetching pinned git toolchain"
bash "${ROOT}/scripts/gitengine-ensure.sh" || {
  echo "warning: gitengine:ensure failed — run ./task gitengine:fetch, or expect GIT_ENGINE_UNAVAILABLE" >&2
}

echo "==> Generating third-party notices"
bash "${ROOT}/scripts/licenses-notices.sh"

echo ""
echo "Dev setup complete."
echo "  • Pre-commit: no-op (suite gates are plan-driven)"
echo "  • Plan handoff: ./task check-fast once; closeouts: ./task check once"
echo "  • In this repo: ./task, or bare task when you already use direnv"
echo "  • Credentials: development builds use an encrypted vault plus a private"
echo "    development identity file. Release macOS builds keep that identity in"
echo "    Keychain; Linux and Windows use the app password."
echo ""
if command -v direnv >/dev/null 2>&1; then
	direnv exec "${ROOT}" task --version
else
	"${PW_BIN_DIR}/task" --version
fi
