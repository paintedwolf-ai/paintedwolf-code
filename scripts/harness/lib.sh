#!/usr/bin/env bash

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS="$(cd "${HARNESS_DIR}/.." && pwd)"
ROOT="$(cd "${SCRIPTS}/.." && pwd)"

# The process ID seeds the port search.
_harness_default_slot() {
  if [[ -n "${LYCAON_HARNESS_SLOT:-}" ]]; then
    if [[ ! "${LYCAON_HARNESS_SLOT}" =~ ^[0-9]+$ ]] || (( LYCAON_HARNESS_SLOT >= 500 )); then
      echo "error: LYCAON_HARNESS_SLOT must be an integer from 0 through 499" >&2
      return 1
    fi
    printf '%s' "${LYCAON_HARNESS_SLOT}"
    return 0
  fi
  local slot=$(( $$ % 500 ))
  if command -v lsof >/dev/null 2>&1; then
    local tries=0
    while (( tries < 50 )); do
      if ! lsof -ti "TCP:$(( 8800 + slot ))" -sTCP:LISTEN >/dev/null 2>&1 &&
         ! lsof -ti "TCP:$(( 14300 + slot ))" -sTCP:LISTEN >/dev/null 2>&1; then
        break
      fi
      slot=$(( (slot + 1) % 500 ))
      tries=$(( tries + 1 ))
    done
  fi
  printf '%s' "${slot}"
}
if [[ -z "${LYCAON_E2E_ADDR:-}" || -z "${LYCAON_E2E_VITE_PORT:-}" ]]; then
  _HARNESS_SLOT="$(_harness_default_slot)"
  : "${LYCAON_E2E_ADDR:=127.0.0.1:$(( 8800 + _HARNESS_SLOT ))}"
  : "${LYCAON_E2E_VITE_PORT:=$(( 14300 + _HARNESS_SLOT ))}"
fi
export LYCAON_E2E_ADDR LYCAON_E2E_VITE_PORT
# shellcheck source=scripts/e2e/env.sh
source "${SCRIPTS}/e2e/env.sh"

HARNESS_VITE_PID=""
HARNESS_PROJECT_ID=""
HARNESS_PROJECT_DIR=""
HARNESS_MODE="mock"
HARNESS_PROVIDER=""
HARNESS_MODEL=""
HARNESS_LEASE_TOKEN=""
HARNESS_LEASE_FILE=".harness-lease"

_harness_process_started_at() {
  ps -p "$1" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//'
}

_harness_resolve_mode() {
  local interactive="${1:-0}"
  if [[ "${LYCAON_LLM_MANUAL:-0}" == "1" ]]; then
    HARNESS_MODE="manual"
  elif [[ "${LYCAON_HARNESS_REAL:-0}" == "1" || -n "${LYCAON_HARNESS_MODEL:-}" ]]; then
    HARNESS_MODE="real"
    HARNESS_MODEL="${LYCAON_HARNESS_MODEL:-}"
    HARNESS_PROVIDER="${LYCAON_HARNESS_PROVIDER:-}"
  elif [[ "${interactive}" == "1" ]]; then
    HARNESS_MODE="manual"
  else
    HARNESS_MODE="mock"
  fi
}

_harness_set_model() {
  [[ -n "${HARNESS_PROJECT_ID:-}" ]] || { echo "warn: no Harness project to scope model to" >&2; return 0; }
  local cur provider="${HARNESS_PROVIDER}" payload
  cur="$(curl -sf -H "Authorization: Bearer ${LYCAON_E2E_TOKEN}" \
    "${LYCAON_E2E_API_URL}/v1/settings/model-policy")" || cur="{}"
  if [[ -z "${provider}" ]] && command -v jq >/dev/null 2>&1; then
    provider="$(printf '%s' "${cur}" | jq -r '.coordinator.provider_id // empty')"
  fi
  HARNESS_PROVIDER="${provider}"
  if command -v jq >/dev/null 2>&1; then
    payload="$(printf '%s' "${cur}" | jq \
      --arg p "${provider}" --arg m "${HARNESS_MODEL}" '
        (if type=="object" then . else {} end)
        | .coordinator={provider_id:$p,model:$m}
        | .lite={provider_id:$p,model:$m}
        | .agent_pool=((.agent_pool // {selection:"fixed"}) + {models:[{provider_id:$p,model:$m}]})')"
  else
    payload="{\"coordinator\":{\"provider_id\":\"${provider}\",\"model\":\"${HARNESS_MODEL}\"},\"lite\":{\"provider_id\":\"${provider}\",\"model\":\"${HARNESS_MODEL}\"},\"agent_pool\":{\"selection\":\"fixed\",\"models\":[{\"provider_id\":\"${provider}\",\"model\":\"${HARNESS_MODEL}\"}]}}"
  fi
  curl -sf -X PATCH -H "Authorization: Bearer ${LYCAON_E2E_TOKEN}" \
    -H "Content-Type: application/json" -d "${payload}" \
    "${LYCAON_E2E_API_URL}/v1/settings/model-policy?project_id=${HARNESS_PROJECT_ID}" >/dev/null || {
    echo "warn: model policy update failed (model ${HARNESS_MODEL} may be invalid for provider ${provider:-<none>})" >&2
  }
}

_harness_json_field() {
  local field="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r ".${field} // empty"
  else
    sed -n "s/.*\"${field}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" | head -n1
  fi
}

_harness_seed_project() {
  local fixture="${ROOT}/lycaon/test/fixtures/e2e/minimal-go-project"
  local dest="${LYCAON_E2E_STATE_DIR}/minimal-go-project"
  local resp
  resp="$(python3 "${HARNESS_DIR}/seed_project.py" "${fixture}" "${dest}")"
  HARNESS_PROJECT_DIR="$(cd "${dest}" && pwd -P)"
  HARNESS_PROJECT_ID="$(printf '%s' "${resp}" | _harness_json_field id)"
}

_harness_require_free_port() {
  local port="$1" label="$2"
  command -v lsof >/dev/null 2>&1 || return 0
  local pids
  pids="$(lsof -ti "TCP:${port}" -sTCP:LISTEN 2>/dev/null || true)"
  [[ -z "${pids}" ]] || {
    echo "error: ${label} port ${port} already in use (pid ${pids//$'\n'/ })." >&2
    echo "       Stop the other process, or set LYCAON_E2E_ADDR / LYCAON_E2E_VITE_PORT." >&2
    return 1
  }
}

harness_up() {
  _harness_require_free_port "${LYCAON_E2E_ADDR##*:}" sidecar
  _harness_require_free_port "${LYCAON_E2E_VITE_PORT}" Vite

  harness_reap_stale_states "${TMPDIR:-/tmp}"
  if [[ -z "${LYCAON_E2E_STATE_DIR:-}" ]]; then
    LYCAON_E2E_STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/lycaon-harness.XXXXXX")"
    export LYCAON_E2E_STATE_DIR
  fi
  mkdir -p "${LYCAON_E2E_STATE_DIR}"
  harness_claim_state "${LYCAON_E2E_STATE_DIR}"

  # The proxy gives browser requests one origin.
  export VITE_LYCAON_PROXY=1
  export VITE_LYCAON_PROXY_TARGET="${LYCAON_E2E_API_URL}"
  export VITE_LYCAON_API_TOKEN="${LYCAON_E2E_TOKEN}"

  local interactive=0
  [[ "${1:-}" == "--interactive" ]] && interactive=1

  _harness_resolve_mode "${interactive}"
  export LYCAON_HARNESS_MODE="${HARNESS_MODE}"
  case "${HARNESS_MODE}" in
    real|manual)
      export LYCAON_LLM_MOCK=0
      [[ "${HARNESS_MODE}" == "manual" ]] && export LYCAON_LLM_MANUAL=1
      ;;
  esac

  if [[ "${1:-}" == "--interactive" ]]; then
    (cd "${ROOT}" && ./task den:harness -- --prepare)
    bash "${SCRIPTS}/e2e-sidecar-bg.sh" --reuse-built
  else
    bash "${SCRIPTS}/e2e-sidecar-bg.sh"
  fi
  _harness_seed_project
  # An empty model keeps the saved default.
  if [[ "${HARNESS_MODE}" == "real" && -n "${HARNESS_MODEL}" ]]; then
    _harness_set_model
  fi
}

harness_vite_bg() {
  bash "${SCRIPTS}/e2e-vite.sh" >"${LYCAON_E2E_STATE_DIR}/vite.log" 2>&1 &
  HARNESS_VITE_PID=$!
  local tries="${LYCAON_E2E_HEALTH_TRIES:-90}"
  for _ in $(seq 1 "${tries}"); do
    if curl -sf --max-time 2 "${LYCAON_E2E_BASE_URL}" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "${HARNESS_VITE_PID}" 2>/dev/null; then
      echo "error: Vite exited early — see ${LYCAON_E2E_STATE_DIR}/vite.log" >&2
      return 1
    fi
    sleep 1
  done
  echo "error: Vite not reachable at ${LYCAON_E2E_BASE_URL}" >&2
  return 1
}

# Captured directories can be read-only.
_harness_remove_state_tree() {
  local state_dir="${1:-}"
  [[ -n "${state_dir}" && "${state_dir}" != "/" ]] || {
    echo "error: refusing to remove an empty or root harness state path" >&2
    return 1
  }
  [[ -e "${state_dir}" ]] || return 0

  chmod u+rwx "${state_dir}" || return 1
  find "${state_dir}" -type d -exec chmod u+rwx {} + || return 1
  rm -rf "${state_dir}"
}

harness_claim_state() {
  local state_dir="$1" marker holder_pid holder_started current_started stale_token
  local owner_pid="${HARNESS_SUPERVISOR_PID:-$$}"
  marker="${state_dir}/${HARNESS_LEASE_FILE}"
  if [[ -f "$marker" ]]; then
    holder_pid="$(sed -n '1p' "$marker" 2>/dev/null || true)"
    if [[ "$holder_pid" =~ ^[0-9]+$ ]] && kill -0 "$holder_pid" 2>/dev/null; then
      holder_started="$(sed -n '3p' "$marker" 2>/dev/null || true)"
      current_started="$(_harness_process_started_at "$holder_pid")"
      if [[ -z "$holder_started" || "$holder_started" == "$current_started" ]]; then
        echo "error: harness state is leased by live pid ${holder_pid}: ${state_dir}" >&2
        return 1
      fi
    fi
    stale_token="$(sed -n '2p' "$marker" 2>/dev/null || true)"
    harness_remove_leased_state "$state_dir" "$stale_token"
    mkdir -p "$state_dir"
  fi
  HARNESS_LEASE_TOKEN="${HARNESS_SUPERVISOR_TOKEN:-$$-$(date +%s)-${RANDOM:-0}}"
  printf '%s\n%s\n%s\n' "$owner_pid" "$HARNESS_LEASE_TOKEN" \
    "$(_harness_process_started_at "$owner_pid")" > "${marker}.tmp.$$"
  mv "${marker}.tmp.$$" "$marker"
}

harness_remove_leased_state() {
  local state_dir="$1" token="$2" marker actual
  marker="${state_dir}/${HARNESS_LEASE_FILE}"
  [[ -f "$marker" ]] || {
    echo "error: refusing to remove unleased harness state: ${state_dir}" >&2
    return 1
  }
  actual="$(sed -n '2p' "$marker" 2>/dev/null || true)"
  [[ -n "$token" && "$actual" == "$token" ]] || {
    echo "error: harness lease changed; state was not removed: ${state_dir}" >&2
    return 1
  }
  _harness_remove_state_tree "$state_dir"
}

harness_reap_stale_states() {
  local base="${1:-${TMPDIR:-/tmp}}" state_dir marker holder_pid holder_started current_started token
  [[ -d "$base" ]] || return 0
  while IFS= read -r -d '' state_dir; do
    marker="${state_dir}/${HARNESS_LEASE_FILE}"
    [[ -f "$marker" ]] || continue
    holder_pid="$(sed -n '1p' "$marker" 2>/dev/null || true)"
    token="$(sed -n '2p' "$marker" 2>/dev/null || true)"
    if [[ "$holder_pid" =~ ^[0-9]+$ ]] && kill -0 "$holder_pid" 2>/dev/null; then
      holder_started="$(sed -n '3p' "$marker" 2>/dev/null || true)"
      current_started="$(_harness_process_started_at "$holder_pid")"
      if [[ -z "$holder_started" || "$holder_started" == "$current_started" ]]; then
        continue
      fi
    fi
    harness_remove_leased_state "$state_dir" "$token" || true
  done < <(find "$base" -maxdepth 1 -type d -name 'lycaon-harness.*' -print0)
}

harness_release_state() {
  local state_dir="$1" marker actual
  marker="${state_dir}/${HARNESS_LEASE_FILE}"
  [[ -f "$marker" ]] || return 0
  actual="$(sed -n '2p' "$marker" 2>/dev/null || true)"
  if [[ -n "$HARNESS_LEASE_TOKEN" && "$actual" == "$HARNESS_LEASE_TOKEN" ]]; then
    rm -f "$marker"
  fi
}

harness_banner() {
  local llm_line
  case "${HARNESS_MODE}" in
    real)
      if [[ -n "${HARNESS_MODEL}" ]]; then
        llm_line="real provider — ${HARNESS_PROVIDER:-?} / ${HARNESS_MODEL} (project-scoped; global config untouched)"
      else
        llm_line="real provider — using throwaway copies of the Den app's saved keys + model"
      fi
      ;;
    manual) llm_line="play both sides — you supply completions via __harness.llm" ;;
    *)      llm_line="mock (deterministic & keyless)" ;;
  esac
  cat >&2 <<EOF

  Den harness ready (native stack, no Docker)

    open:     ${LYCAON_E2E_BASE_URL}
    token:    ${LYCAON_E2E_TOKEN}
    sidecar:  ${LYCAON_E2E_API_URL}  (same-origin via Vite proxy → no CORS)
    listen:   ${LYCAON_E2E_ADDR}     (LYCAON_E2E_ADDR — host:port, not a URL)
    lifetime: ${LYCAON_HARNESS_TIMEOUT_SECONDS:-10800}s (0 disables the deadline)
    llm:      ${llm_line}
    project:  "Harness"  id=${HARNESS_PROJECT_ID:-<unknown>}
              ${HARNESS_PROJECT_DIR}

  Drive it: point a browser tool (preview_* / Chrome MCP) at the URL above.
  A semantic driver is installed on the page — run in the console:

    await __harness.help()                       // list every method
    await __harness.openProject('Harness')
    await __harness.newSession('look at the repo')
    await __harness.prompt('say hello')           // sends + waits for the reply
    await __harness.transcript()                  // read it back

  The "Harness" project is also in the welcome launcher (served from
  /v1/projects), so you can click through the UI directly instead.

  Author tests into lycaon-den/e2e/ using e2e/helpers.ts, then run them with:
    ./task den:harness:test -- --grep "<your test name>"

EOF
  if [[ "${HARNESS_MODE}" == "manual" ]]; then
    cat >&2 <<'EOF'
  Play both sides — you author the assistant's turns:
    await __harness.llm.manual()                 // stop auto-replying
    await __harness.sendPrompt('plan a refactor')
    await __harness.llm.pending()                // read the blocked request
    await __harness.llm.respond({ text: 'Here is the plan…' })
    await __harness.llm.auto('canned reply')     // resume auto-replies

EOF
  fi
}
