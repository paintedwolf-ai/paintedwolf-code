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

# SIGKILL and restart the sidecar container on its fixed host port.
e2e_docker_crash_restart_sidecar() {
  local container port
  container="$(docker compose -f "${LYCAON_E2E_COMPOSE_FILE}" -p "${LYCAON_E2E_DOCKER_PROJECT}" ps -q sidecar)"
  [[ -n "${container}" ]] || {
    echo "error: no sidecar container in project ${LYCAON_E2E_DOCKER_PROJECT}" >&2
    return 1
  }
  docker kill --signal KILL "${container}" >/dev/null
  docker wait "${container}" >/dev/null
  docker start "${container}" >/dev/null
  port="$(e2e_docker_compose_port sidecar 8787)"
  [[ "127.0.0.1:${port}" == "${LYCAON_E2E_ADDR}" ]] || {
    echo "error: restarted sidecar listens on ${port}, not ${LYCAON_E2E_ADDR}" >&2
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
