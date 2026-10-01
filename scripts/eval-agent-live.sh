#!/usr/bin/env bash
# Live evaluations use isolated configuration and require --allow-live.
set -euo pipefail
main() {
  if [[ "${1:-}" != "--allow-live" ]]; then
    echo 'Usage: bash scripts/eval-agent-live.sh --allow-live --source-config DIR --provider ID --model ID --label NAME [--cases IDS] [--runs N] [--timeout DURATION] [--wait-for-input] [--recall-stress] [--config-root MODULE] [--engine BINARY] [--driver BINARY] [--suite FILE] [--worker-provider ID --worker-model ID] [--application FILE] [--expected-plan FILE] [--rate-state-dir ABSOLUTE_DIR] [--out-dir ABSOLUTE_DIR]'
    exit 2
  fi
  shift
  export LYCAON_COMMAND_PATH="${PATH:-}"
  EVAL_SOURCE_CONFIG='' EVAL_PROVIDER='' EVAL_MODEL='' EVAL_LABEL=''
  EVAL_CASES='existing-project-delivery,sandbox-recovery,secret-lifecycle' EVAL_RUNS=1 EVAL_MODULE=''
  EVAL_ENGINE='' EVAL_DRIVER='' EVAL_TIMEOUT='0s'
  EVAL_SUITE='test/fixtures/eval/release-suite.yaml' EVAL_WORKER_PROVIDER='' EVAL_WORKER_MODEL=''
  EVAL_APPLICATION='' EVAL_EXPECTED_PLAN=''
  EVAL_DESTINATION='' EVAL_RATE_STATE_DIR=''
  EVAL_RECALL_STRESS=false
  EVAL_LIVE_ARGS=(--allow-live)
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == '--recall-stress' ]]; then
      EVAL_RECALL_STRESS=true
      shift
      continue
    fi
    if [[ "$1" == '--wait-for-input' ]]; then
      EVAL_LIVE_ARGS+=(--wait-for-input)
      shift
      continue
    fi
    [[ $# -ge 2 ]] || { echo "Missing value for $1"; exit 2; }
    case "$1" in
      --source-config) EVAL_SOURCE_CONFIG="$2" ;;
      --provider) EVAL_PROVIDER="$2" ;;
      --model) EVAL_MODEL="$2" ;;
      --label) EVAL_LABEL="$2" ;;
      --cases) EVAL_CASES="$2" ;;
      --runs) EVAL_RUNS="$2" ;;
      --timeout) EVAL_TIMEOUT="$2" ;;
      --config-root) EVAL_MODULE="$2" ;;
      --engine) EVAL_ENGINE="$2" ;;
      --driver) EVAL_DRIVER="$2" ;;
      --suite) EVAL_SUITE="$2" ;;
      --worker-provider) EVAL_WORKER_PROVIDER="$2" ;;
      --worker-model) EVAL_WORKER_MODEL="$2" ;;
      --application) EVAL_APPLICATION="$2" ;;
      --expected-plan) EVAL_EXPECTED_PLAN="$2" ;;
      --rate-state-dir) EVAL_RATE_STATE_DIR="$2" ;;
      --out-dir) EVAL_DESTINATION="$2" ;;
      *) echo "Unknown option $1"; exit 2 ;;
    esac
    shift 2
  done
  [[ -n "$EVAL_SOURCE_CONFIG" && -n "$EVAL_PROVIDER" && -n "$EVAL_MODEL" && -n "$EVAL_LABEL" ]] || { echo 'Source config, provider, model, and label are required.'; exit 2; }
  if [[ "$EVAL_RECALL_STRESS" == true ]]; then
    [[ "$EVAL_CASES" == 'recall' ]] || { echo '--recall-stress requires --cases recall; run normal outcomes separately.'; exit 2; }
  elif [[ ",$EVAL_CASES," == *,recall,* ]]; then
    echo 'Run recall separately with --cases recall --recall-stress; normal outcomes must use the shipped compaction settings.'
    exit 2
  fi
  if [[ -n "$EVAL_WORKER_PROVIDER" && -z "$EVAL_WORKER_MODEL" || -z "$EVAL_WORKER_PROVIDER" && -n "$EVAL_WORKER_MODEL" ]]; then
    echo 'Worker provider and model must be supplied together.'; exit 2
  fi
  EVAL_WORKER_PROVIDER="${EVAL_WORKER_PROVIDER:-$EVAL_PROVIDER}"
  EVAL_WORKER_MODEL="${EVAL_WORKER_MODEL:-$EVAL_MODEL}"
  [[ -f Taskfile.yml && -f lycaon/go.mod ]] || { echo 'Run from the repository root.'; exit 2; }
  local prerequisite
  for prerequisite in jq node; do
    if ! command -v "$prerequisite" >/dev/null; then
      echo "Missing evaluation prerequisite: $prerequisite" >&2
      exit 1
    fi
  done
  # shellcheck source=scripts/artifact-paths.sh
  source "$PWD/scripts/artifact-paths.sh"
  if [[ -z "$EVAL_ENGINE" ]]; then
    ./task build:lycaon-dev
    EVAL_ENGINE="${PW_BUILD_DIR}/lycaon-dev"
  fi
  [[ -x "$EVAL_ENGINE" ]] || { echo 'Selected engine is not executable.'; exit 2; }
  if [[ -z "$EVAL_DRIVER" ]]; then
    ./task eval:tool-usage BUILD_ONLY=true
    EVAL_DRIVER="${PW_BUILD_DIR}/lycaon-debug"
  fi
  [[ "$EVAL_DRIVER" == /* && -x "$EVAL_DRIVER" ]] || { echo 'Selected driver must be an absolute executable path.'; exit 2; }
  if [[ -n "$EVAL_DESTINATION" ]]; then
    [[ "$EVAL_DESTINATION" == /* ]] || { echo 'Output directory must be absolute.'; exit 2; }
    mkdir "$EVAL_DESTINATION"
    EVAL_RUN_DIR="$EVAL_DESTINATION"
  else
    EVAL_RUN_DIR="$(mktemp -d /tmp/paintedwolf-agent-eval.XXXXXX)"
  fi
  chmod 700 "$EVAL_RUN_DIR"
  mkdir "$EVAL_RUN_DIR/config" "$EVAL_RUN_DIR/module"
  EVAL_PID='' EVAL_DRIVER_PID=''
  eval_cleanup() {
    if [[ -n "$EVAL_DRIVER_PID" ]]; then
      kill "$EVAL_DRIVER_PID" 2>/dev/null || true
      wait "$EVAL_DRIVER_PID" 2>/dev/null || true
    fi
    if [[ -n "$EVAL_PID" ]]; then
      kill "$EVAL_PID" 2>/dev/null || true
      wait "$EVAL_PID" 2>/dev/null || true
    fi
    rm -f "$EVAL_RUN_DIR/config/credential-vault.age" "$EVAL_RUN_DIR/config/.credential-vault-development-identity" \
      "$EVAL_RUN_DIR/config/providers.local.yaml" "$EVAL_RUN_DIR/config/model-policy.yaml" "$EVAL_RUN_DIR/config/api.token"
  }
  trap eval_cleanup EXIT
  trap 'exit 130' INT TERM
  for eval_config_file in providers.local.yaml model-policy.yaml credential-vault.age .credential-vault-development-identity; do
    if [[ -f "$EVAL_SOURCE_CONFIG/$eval_config_file" ]]; then
      cp "$EVAL_SOURCE_CONFIG/$eval_config_file" "$EVAL_RUN_DIR/config/$eval_config_file"
    fi
  done
  python3 scripts/coordinator-benchmark/capture_storage.py --engine "$EVAL_ENGINE" \
    --catalog "${EVAL_MODULE:-$PWD/lycaon}/config" --capture "$EVAL_RUN_DIR"
  shasum -a 256 "$EVAL_RUN_DIR/engine" >"$EVAL_RUN_DIR/engine.sha256"
  # Recall pressure belongs only to its isolated stress run.
  if [[ "$EVAL_RECALL_STRESS" == true ]]; then
    node - "$EVAL_RUN_DIR/module/config/packs/painted-wolf/platform/host/compaction.yaml" <<'NODE'
const fs = require('node:fs');
const path = process.argv[2];
let text = fs.readFileSync(path, 'utf8');
text = text.replace(/^target_tokens_pct: \d+$/m, 'target_tokens_pct: 1');
text = text.replace(/^keep_recent_messages: \d+$/m, 'keep_recent_messages: 2');
fs.writeFileSync(path, text);
NODE
  fi
  EVAL_PORT="$(node -e 'const s=require("node:net").createServer();s.listen(0,"127.0.0.1",()=>{process.stdout.write(String(s.address().port));s.close();});')"
  export LYCAON_CONFIG_DIR="$EVAL_RUN_DIR/config" LYCAON_CONFIG_ROOT="$EVAL_RUN_DIR/module"
  export LYCAON_ADDR="127.0.0.1:$EVAL_PORT" LYCAON_API_TOKEN="$(openssl rand -hex 24)"
  printf '%s' "$LYCAON_API_TOKEN" >"$EVAL_RUN_DIR/config/api.token"
  chmod 600 "$EVAL_RUN_DIR/config/api.token"
  export LYCAON_DEV=1 LYCAON_HARNESS=1 LYCAON_LLM_MOCK=0
  if [[ -n "$EVAL_RATE_STATE_DIR" ]]; then
    [[ "$EVAL_RATE_STATE_DIR" == /* ]] || { echo 'Provider rate state directory must be absolute.'; exit 2; }
    export LYCAON_LLM_RATE_STATE_DIR="$EVAL_RATE_STATE_DIR"
  else
    unset LYCAON_LLM_RATE_STATE_DIR
  fi
  export LYCAON_ENGINE_ROOT="$PWD/lycaon-den/src-tauri/engine-root"
  export LYCAON_LLM_DEBUG=1 LYCAON_SESSION_DEBUG=1 LYCAON_TOOL_DEBUG=1
  export LYCAON_LLM_DEBUG_FILE="$EVAL_RUN_DIR/llm-requests.jsonl"
  export LYCAON_SESSION_DEBUG_FILE="$EVAL_RUN_DIR/sessions.jsonl"
  export LYCAON_TOOL_DEBUG_FILE="$EVAL_RUN_DIR/tool-invocations.jsonl"
  "$EVAL_RUN_DIR/engine" serve --db "$EVAL_RUN_DIR/store.db" >"$EVAL_RUN_DIR/sidecar.log" 2>&1 &
  EVAL_PID=$!
  EVAL_URL="http://$LYCAON_ADDR"
  echo "Isolated live evaluation: $EVAL_RUN_DIR (owned sidecar pid $EVAL_PID)"
  while true; do
    kill -0 "$EVAL_PID" 2>/dev/null || { echo 'Sidecar exited; inspect sidecar.log.'; exit 1; }
    if curl --connect-timeout 2 --max-time 5 -sf "$EVAL_URL/health" >/dev/null; then break; fi
    sleep 0.5
  done
  python3 scripts/coordinator-benchmark/bootstrap_policy.py --url "$EVAL_URL" \
    --provider "$EVAL_PROVIDER" --model "$EVAL_MODEL" \
    --worker-provider "$EVAL_WORKER_PROVIDER" --worker-model "$EVAL_WORKER_MODEL" --out "$EVAL_RUN_DIR"
  EVAL_CAPTURE_ARGS=("$EVAL_RUN_DIR" --time-limit "$EVAL_TIMEOUT")
  if [[ -n "$EVAL_APPLICATION" ]]; then EVAL_CAPTURE_ARGS+=(--application "$EVAL_APPLICATION"); fi
  if [[ -n "$EVAL_EXPECTED_PLAN" ]]; then EVAL_CAPTURE_ARGS+=(--expected-plan "$EVAL_EXPECTED_PLAN"); fi
  python3 scripts/coordinator-benchmark/capture.py "${EVAL_CAPTURE_ARGS[@]}"
  if [[ "$EVAL_SUITE" == /* ]]; then
    cp "$EVAL_SUITE" "$EVAL_RUN_DIR/suite.json"
  else
    cp "$PWD/lycaon/$EVAL_SUITE" "$EVAL_RUN_DIR/suite.json"
  fi
  EVAL_STATUS=0
  # The driver resolves repository-relative fixture paths from the Go module.
  (cd lycaon && "$EVAL_DRIVER" eval tool-usage "${EVAL_LIVE_ARGS[@]}" --suite "$EVAL_SUITE" \
    --addr "$EVAL_URL" --expect-model "$EVAL_MODEL" --label "$EVAL_LABEL" --cases "$EVAL_CASES" \
    --runs "$EVAL_RUNS" --state-db "$EVAL_RUN_DIR/store.db" --timeout "$EVAL_TIMEOUT" --capture "$EVAL_RUN_DIR" --out "$EVAL_RUN_DIR/report.json") &
  EVAL_DRIVER_PID=$!
  while kill -0 "$EVAL_DRIVER_PID" 2>/dev/null; do
    if ! kill -0 "$EVAL_PID" 2>/dev/null; then
      kill "$EVAL_DRIVER_PID" 2>/dev/null || true
      break
    fi
    sleep 0.5
  done
  wait "$EVAL_DRIVER_PID" || EVAL_STATUS=$?
  EVAL_DRIVER_PID=''

  # Drain owned capture writers before measuring the final provider response.
  kill "$EVAL_PID" 2>/dev/null || true
  wait "$EVAL_PID" || true
  EVAL_PID=''
  "$EVAL_DRIVER" eval tool-usage --refresh "$EVAL_RUN_DIR/report.json"
  return "$EVAL_STATUS"
}
main "$@"
