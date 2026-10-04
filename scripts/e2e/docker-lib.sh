# Shared helpers for end-to-end test stacks.
# Source this file from an end-to-end script.

e2e_docker_available() {
  command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1
}

# Project names start with a letter and use lowercase alphanumerics, hyphens, or underscores.
e2e_docker_random_project() {
  local prefix="${1:-lycae2e}"
  local hex
  if command -v openssl >/dev/null 2>&1; then
    hex="$(openssl rand -hex 8)"
  else
    hex="$(date +%s)$RANDOM"
  fi
  echo "${prefix}${hex}"
}

e2e_docker_compose_port() {
  local service="$1"
  local container_port="$2"
  docker compose -f "${LYCAON_E2E_COMPOSE_FILE}" -p "${LYCAON_E2E_DOCKER_PROJECT}" port "${service}" "${container_port}" \
    | sed -E 's/^127\.0\.0\.1://; s/^.*://'
}

e2e_free_local_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

# Specs serve fixtures on host loopback and call the engine's loopback
# callbacks, so Linux runs the stack on the host's network. Docker Desktop
# shares it only with host networking enabled: set LYCAON_E2E_NETWORK_MODE=host.
e2e_docker_select_network() {
  if [[ -z "${LYCAON_E2E_NETWORK_MODE+set}" && "$(uname -s)" == Linux ]]; then
    LYCAON_E2E_NETWORK_MODE=host
  fi
  export LYCAON_E2E_NETWORK_MODE="${LYCAON_E2E_NETWORK_MODE:-}"
  e2e_docker_host_network || return 0
  # Services share the host's ports, so each listens on loopback at a probed port.
  export LYCAON_E2E_LISTEN_HOST=127.0.0.1
  export LYCAON_E2E_SIDECAR_LISTEN_PORT="${LYCAON_E2E_SIDECAR_PORT}"
  export LYCAON_E2E_VITE_LISTEN_PORT="${LYCAON_E2E_VITE_LISTEN_PORT:-$(e2e_free_local_port)}"
  export LYCAON_E2E_MODEL_FIXTURE_PORT="${LYCAON_E2E_MODEL_FIXTURE_PORT:-$(e2e_free_local_port)}"
  export LYCAON_E2E_PROXY_TARGET="http://127.0.0.1:${LYCAON_E2E_SIDECAR_PORT}"
  local providers="${LYCAON_E2E_CONFIG_DIR:?}/providers.local.yaml"
  sed -e "s#http://model-fixture:11434#http://127.0.0.1:${LYCAON_E2E_MODEL_FIXTURE_PORT}#" \
    "${providers}" >"${providers}.tmp"
  mv "${providers}.tmp" "${providers}"
}

e2e_docker_host_network() {
  [[ "${LYCAON_E2E_NETWORK_MODE:-}" == host ]]
}

# SIGKILL the container's recorded sidecar; its serve loop starts the next one.
e2e_docker_crash_restart_sidecar() {
  local container previous current i
  container="$(docker compose -f "${LYCAON_E2E_COMPOSE_FILE}" -p "${LYCAON_E2E_DOCKER_PROJECT}" ps -q sidecar)"
  [[ -n "${container}" ]] || {
    echo "error: no sidecar container in project ${LYCAON_E2E_DOCKER_PROJECT}" >&2
    return 1
  }
  previous="$(docker exec "${container}" cat /tmp/lycaon.pid)"
  [[ "${previous}" =~ ^[0-9]+$ ]] || {
    echo "error: sidecar container recorded no pid" >&2
    return 1
  }
  docker exec "${container}" sh -c "kill -KILL ${previous}"
  # A new pid means the killed process has exited, so health answers come from its successor.
  for ((i = 1; i <= 600; i++)); do
    current="$(docker exec "${container}" cat /tmp/lycaon.pid 2>/dev/null || true)"
    [[ -n "${current}" && "${current}" != "${previous}" ]] && break
    sleep 0.1
  done
  [[ -n "${current}" && "${current}" != "${previous}" ]] || {
    echo "error: sidecar pid ${previous} was not replaced" >&2
    return 1
  }
  e2e_wait_http "${LYCAON_E2E_HEALTH_URL}" "sidecar /health" 300
}

e2e_refresh_derived_urls() {
  LYCAON_E2E_BASE_URL="http://${LYCAON_E2E_VITE_HOST}:${LYCAON_E2E_VITE_PORT}"
  LYCAON_E2E_API_URL="http://${LYCAON_E2E_ADDR}"
  LYCAON_E2E_HEALTH_URL="${LYCAON_E2E_API_URL}/health"
  export LYCAON_E2E_BASE_URL LYCAON_E2E_API_URL LYCAON_E2E_HEALTH_URL
}

e2e_wait_http() {
  local url="$1"
  local label="${2:-endpoint}"
  local tries="${3:-90}"
  local sleep_s="${4:-1}"
  local i
  for ((i = 1; i <= tries; i++)); do
    if curl -sf --max-time 2 "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep "${sleep_s}"
  done
  echo "error: ${label} not ready at ${url} after ${tries} attempts" >&2
  return 1
}

# Owned project names receive a random suffix at runtime.
e2e_docker_project_prefixes() {
  printf '%s\n' lycae2e
}

e2e_docker_persist_project() {
  local project="$1"
  local state_dir="${LYCAON_E2E_STATE_DIR:-}"
  [[ -n "${state_dir}" && -n "${project}" ]] || return 0
  printf '%s\n' "${project}" >"${state_dir}/docker-project"
}

e2e_docker_read_persisted_project() {
  local state_dir="${LYCAON_E2E_STATE_DIR:-}"
  [[ -n "${state_dir}" && -f "${state_dir}/docker-project" ]] || return 1
  tr -d '[:space:]' <"${state_dir}/docker-project"
}

# Tear down one project and its named volumes.
e2e_docker_teardown_project() {
  local compose_file="$1"
  local project="$2"
  [[ -n "${compose_file}" && -n "${project}" ]] || return 0
  if ! command -v docker >/dev/null 2>&1; then
    return 0
  fi
  echo "e2e-docker-teardown: project=${project}" >&2
  docker compose -f "${compose_file}" -p "${project}" down -v --remove-orphans 2>/dev/null || true
}

# Best-effort purge of orphaned end-to-end projects.
e2e_docker_purge_orphan_projects() {
  local root="$1"
  local e2e_compose="${root}/lycaon/test/fixtures/e2e/docker-compose.e2e.yml"
  local proj prefix

  if ! e2e_docker_available; then
    return 0
  fi

  while IFS= read -r proj; do
    [[ -z "${proj}" ]] && continue
    for prefix in $(e2e_docker_project_prefixes); do
      if [[ "${proj}" == "${prefix}"* ]]; then
        e2e_docker_teardown_project "${e2e_compose}" "${proj}"
        break
      fi
    done
  done < <(docker compose ls -a --format '{{.Name}}' 2>/dev/null || true)
}
