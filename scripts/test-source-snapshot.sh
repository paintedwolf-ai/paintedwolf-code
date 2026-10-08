#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=artifact-paths.sh
source "${ROOT}/scripts/artifact-paths.sh"
SNAPSHOT_PARENT="${PW_TEST_SNAPSHOT_ROOT:-/tmp/paintedwolf-source-snapshots-${UID}}"
CAPTURE_ATTEMPTS="${PW_TEST_SNAPSHOT_CAPTURE_ATTEMPTS:-20}"
CAPTURE_LOCK_TIMEOUT="${PW_TEST_SNAPSHOT_LOCK_TIMEOUT:-900}"
CAPTURE_LOCKDIR="${PW_LOCK_ROOT}/source-capture.lockdir"
LOCK_SH="${ROOT}/scripts/digest-run-lock.sh"
# shellcheck source=digest-run-lock.sh
source "${LOCK_SH}"
# shellcheck source=test-run-isolation.sh
source "${ROOT}/scripts/test-run-isolation.sh"
TEST_RUN_ROOT="/tmp/paintedwolf-snapshot-caches-${UID}"

usage() {
  echo "usage: $0 run -- command [args...]" >&2
  echo "       $0 run-commit COMMIT -- command [args...]" >&2
  echo "       $0 capture" >&2
  echo "       $0 holding" >&2
  exit 2
}

snapshot_live() {
  local original="${PW_SOURCE_ROOT_ORIGINAL:-}" head
  [[ -n "${PW_SOURCE_SNAPSHOT_COMMIT:-}" && -n "$original" ]] || return 1
  [[ "$(cd "$ROOT" && pwd -P)" != "$(cd "$original" && pwd -P)" ]] || return 1
  if git -C "$ROOT" symbolic-ref -q HEAD >/dev/null 2>&1; then
    return 1
  fi
  head="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null)" || return 1
  [[ "$head" == "$PW_SOURCE_SNAPSHOT_COMMIT" ]] || return 1
  git -C "$original" cat-file -e "${PW_SOURCE_SNAPSHOT_COMMIT}^{commit}" 2>/dev/null
}

capture_tree() {
  local index="$1" source_index
  rm -f "$index"
  if git -C "$ROOT" ls-files -v | grep -Eq '^[a-z]|^S '; then
    GIT_INDEX_FILE="$index" git -C "$ROOT" read-tree HEAD
  else
    source_index="$(git -C "$ROOT" rev-parse --path-format=absolute --git-path index)"
    cp -p "$source_index" "$index"
  fi
  GIT_INDEX_FILE="$index" git -C "$ROOT" add -A -- .
  GIT_INDEX_FILE="$index" git -C "$ROOT" rm --cached --quiet -r --ignore-unmatch -- lycaon-den/node_modules
  GIT_INDEX_FILE="$index" git -C "$ROOT" write-tree
}

capture_commit() {
  local attempt first_index second_index first_tree second_tree parent commit
  if [[ ! "$CAPTURE_ATTEMPTS" =~ ^[1-9][0-9]*$ ]]; then
    echo "PW_TEST_SNAPSHOT_CAPTURE_ATTEMPTS must be a positive integer" >&2
    exit 2
  fi
  first_index="$(mktemp "${TMPDIR:-/tmp}/paintedwolf-index.XXXXXX")"
  second_index="$(mktemp "${TMPDIR:-/tmp}/paintedwolf-index.XXXXXX")"
  # The EXIT trap can run after these locals leave scope.
  trap "rm -f $(printf '%q ' "$first_index" "$second_index")" EXIT
  for ((attempt = 1; attempt <= CAPTURE_ATTEMPTS; attempt++)); do
    first_tree="$(capture_tree "$first_index")"
    second_tree="$(capture_tree "$second_index")"
    if [[ "$first_tree" == "$second_tree" ]]; then
      parent="$(git -C "$ROOT" rev-parse HEAD)"
      commit="$(
        GIT_AUTHOR_NAME='Painted Wolf tests' \
        GIT_AUTHOR_EMAIL='tests@paintedwolf.local' \
        GIT_COMMITTER_NAME='Painted Wolf tests' \
        GIT_COMMITTER_EMAIL='tests@paintedwolf.local' \
          git -C "$ROOT" commit-tree "$first_tree" -p "$parent" <<<'test source snapshot'
      )"
      printf '%s\n' "$commit"
      exit 0
    fi
    echo "test-source-snapshot: source changed during capture; retrying (${attempt}/${CAPTURE_ATTEMPTS})" >&2
  done
  echo "test-source-snapshot: source did not settle after ${CAPTURE_ATTEMPTS} attempts" >&2
  exit 1
}

prepare_runtime() {
  if [[ -d "$ROOT/lycaon-den/node_modules" && ! -e "$SNAPSHOT_DIR/lycaon-den/node_modules" && ! -L "$SNAPSHOT_DIR/lycaon-den/node_modules" ]]; then
    ln -s "$ROOT/lycaon-den/node_modules" "$SNAPSHOT_DIR/lycaon-den/node_modules"
  fi
  if [[ -d "$ROOT/lycaon-den/src-tauri/binaries" && ! -e "$SNAPSHOT_DIR/lycaon-den/src-tauri/binaries" && ! -L "$SNAPSHOT_DIR/lycaon-den/src-tauri/binaries" ]]; then
    ln -s "$ROOT/lycaon-den/src-tauri/binaries" "$SNAPSHOT_DIR/lycaon-den/src-tauri/binaries"
  fi
  if [[ -f "$ROOT/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" && ! -e "$SNAPSHOT_DIR/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" && ! -L "$SNAPSHOT_DIR/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" ]]; then
    ln -s "$ROOT/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" "$SNAPSHOT_DIR/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md"
  fi
  if [[ -d "$ROOT/lycaon-den/src-tauri/engine-root/gitengine" ]]; then
    local engine_root="$SNAPSHOT_DIR/lycaon-den/src-tauri/engine-root" staged
    mkdir -p "$engine_root"
    staged="$(mktemp -d "$engine_root/.gitengine.XXXXXX")"
    if [[ "$(uname -s)" == Darwin ]]; then
      cp -cpR "$ROOT/lycaon-den/src-tauri/engine-root/gitengine/." "$staged/"
    else
      cp -a --reflink=auto "$ROOT/lycaon-den/src-tauri/engine-root/gitengine/." "$staged/" 2>/dev/null ||
        cp -a "$ROOT/lycaon-den/src-tauri/engine-root/gitengine/." "$staged/"
    fi
    rm -rf "$engine_root/gitengine"
    mv "$staged" "$engine_root/gitengine"
  fi
}

preserve_fuzz_failures() {
  local relative destination
  while IFS= read -r -d '' relative; do
    destination="${PW_SOURCE_ROOT_ORIGINAL}/${relative}"
    if [[ ! -e "$destination" ]]; then
      mkdir -p "$(dirname "$destination")"
      cp -p "$SNAPSHOT_DIR/$relative" "$destination"
      echo "test-source-snapshot: retained fuzz failure ${relative}" >&2
    fi
  done < <(git -C "$SNAPSHOT_DIR" ls-files --others --exclude-standard -z -- ':(glob)**/testdata/fuzz/**')
}

# VARIABLE=pathspec entries identify fixture refreshes to retain after snapshot cleanup.
FIXTURE_REFRESHES=(
  "UPDATE_SCHEMA_LOCK=lycaon/internal/db/schema.sql.lock.json"
  "UPDATE_REPORT_GOLDEN=lycaon/internal/report/testdata/golden"
  "UPDATE_ANCHOR_KICK_GOLDEN=lycaon/internal/coordinator/anchor/testdata/golden/kicks"
  "UPDATE_CITATION_GROUNDING_PARITY_GOLDENS=lycaon/test/fixtures/citation-grounding-parity"
  "UPDATE_CORPUS_BASELINE=lycaon/internal/oar/testdata/corpus_baseline.json"
  "UPDATE_RUST_HOST_WIRE_FIXTURES=lycaon-den/src-tauri/tests/host_wire_fixtures.json"
)

# Refreshes preserve checkout edits made after capture and omit snapshot deletions.
preserve_fixture_refreshes() {
  local entry variable pathspec relative destination
  for entry in "${FIXTURE_REFRESHES[@]}"; do
    variable="${entry%%=*}"
    pathspec="${entry#*=}"
    [[ "${!variable:-}" == "1" ]] || continue
    # Tests can create the fixture path during the run.
    [[ -e "$SNAPSHOT_DIR/$pathspec" ]] || continue
    while IFS= read -r -d '' relative; do
      [[ -f "$SNAPSHOT_DIR/$relative" ]] || continue
      destination="${PW_SOURCE_ROOT_ORIGINAL}/${relative}"
      cmp -s "$SNAPSHOT_DIR/$relative" "$destination" && continue
      if [[ -L "$destination" ]] || {
        if git -C "$SNAPSHOT_DIR" cat-file -e "HEAD:${relative}" 2>/dev/null; then
          ! git -C "$SNAPSHOT_DIR" show "HEAD:${relative}" | cmp -s - "$destination"
        else
          [[ -e "$destination" ]]
        fi
      }; then
        echo "test-source-snapshot: ${relative} changed in the checkout during the run; not overwriting (${variable})" >&2
        continue
      fi
      mkdir -p "$(dirname "$destination")"
      cp -p "$SNAPSHOT_DIR/$relative" "$destination"
      echo "test-source-snapshot: refreshed ${relative} (${variable})" >&2
    done < <(git -C "$SNAPSHOT_DIR" ls-files -z --modified --others --exclude-standard -- "$pathspec")
  done
}

acquire_snapshot_slot() {
  local common key slot=0 status
  common="$(git -C "$ROOT" rev-parse --path-format=absolute --git-common-dir)"
  key="$(python3 "$TEST_RUN_LEASE_HELPER" path-key "$common")"
  mkdir -p "$SNAPSHOT_PARENT"
  while :; do
    SNAPSHOT_DIR="$SNAPSHOT_PARENT/${key}-${slot}"
    SNAPSHOT_LEASE="${SNAPSHOT_DIR}.lock"
    exec 7>"$SNAPSHOT_LEASE"
    if python3 "$TEST_RUN_LEASE_HELPER" try-lock 7; then
      break
    else
      status=$?
      [[ "$status" == 1 ]] || return "$status"
    fi
    exec 7>&-
    slot=$((slot + 1))
  done
  # An unused slot can retain a worktree from an interrupted supervisor, or a
  # checkout whose registration a worktree prune already dropped.
  if [[ -e "$SNAPSHOT_DIR" ]]; then
    git -C "$ROOT" worktree remove --force "$SNAPSHOT_DIR" 2>/dev/null || rm -rf "$SNAPSHOT_DIR"
  fi
}

cleanup_snapshot() {
  exec 7>&-
  exec 7>"$SNAPSHOT_LEASE"
  # Descendant leases protect the source tree until they close.
  if python3 "$TEST_RUN_LEASE_HELPER" try-lock 7; then
    git -C "$ROOT" worktree remove --force "$SNAPSHOT_DIR" >/dev/null 2>&1 || true
  fi
  exec 7>&- 8>&- 9>&-
  test_run_prune_abandoned || true
}

run_commit() {
  local commit="$1"
  shift
  [[ "${1:-}" == "--" ]] || usage
  shift
  [[ $# -gt 0 ]] || usage
  git -C "$ROOT" cat-file -e "${commit}^{commit}"
  acquire_snapshot_slot
  trap cleanup_snapshot EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  git -C "$ROOT" worktree add --quiet --detach --force "$SNAPSHOT_DIR" "$commit"
  test_run_create_isolation "snapshot-cache"
  export PW_TEST_CHECKOUT_CACHE="${TEST_RUN_DIR}/cache"
  # Compiled artifacts survive snapshots; diagnostic paths remain isolated.
  export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
  prepare_runtime
  export PW_SOURCE_ROOT_ORIGINAL="${PW_SOURCE_ROOT_ORIGINAL:-$ROOT}"
  export PW_SOURCE_SNAPSHOT_COMMIT="$commit"
  export PW_TEST_ARTIFACT_ROOT="${PW_TEST_ARTIFACT_ROOT:-$PW_ARTIFACT_ROOT}"
  export OPENGREP_STAGE_DIR="${OPENGREP_STAGE_DIR:-$PW_BUILD_DIR/opengrep-bundle}"
  export OPENGREP_CACHE_DIR="${OPENGREP_CACHE_DIR:-$PW_BIN_DIR/opengrep-artifacts}"
  # Executables built from the snapshot must not replace the checkout's builds.
  export PW_BUILD_DIR="${TEST_RUN_DIR}/build"
  case "$OPENGREP_CACHE_DIR" in
    /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
    *) export OPENGREP_CACHE_DIR="$PWD/$OPENGREP_CACHE_DIR" ;;
  esac
  export CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-$ROOT/lycaon-den/target}"
  cd "$SNAPSHOT_DIR"
  set +e
  "$@"
  local rc=$?
  set -e
  if [[ "$rc" -ne 0 ]]; then
    preserve_fuzz_failures
  fi
  preserve_fixture_refreshes
  exit "$rc"
}

mode="${1:-}"
shift || true
case "$mode" in
  holding)
    snapshot_live
    ;;
  capture)
    capture_commit
    ;;
  run)
    [[ "${1:-}" == "--" ]] || usage
    shift
    [[ $# -gt 0 ]] || usage
    if snapshot_live; then
      exec "$@"
    fi
    mkdir -p "$(dirname "$CAPTURE_LOCKDIR")"
    digest_acquire_lock "$CAPTURE_LOCKDIR" "$CAPTURE_LOCK_TIMEOUT" || exit 1
    trap 'rm -f "${capture_result:-}"; digest_release_lock' EXIT
    capture_result="$(mktemp "${TMPDIR:-/tmp}/paintedwolf-capture.XXXXXX")"
    # Epoch retries replace the attempted result; only a stable read can use it.
    bash "$ROOT/scripts/repo-snapshot-lock.sh" read -- \
      bash -c '"$1" capture >"$2"' capture-result "$0" "$capture_result"
    commit="$(cat "$capture_result")"
    rm -f "$capture_result"
    digest_release_lock
    trap - EXIT
    run_commit "$commit" -- "$@"
    ;;
  run-commit)
    [[ $# -ge 2 ]] || usage
    commit="$1"
    shift
    run_commit "$commit" "$@"
    ;;
  *)
    usage
    ;;
esac
