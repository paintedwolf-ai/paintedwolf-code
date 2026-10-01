#!/usr/bin/env bash
# Full-stack check: does a running scan retain honest status across a sidecar
# shutdown, and are the engine's processes reaped with the host?
#
# Isolation rules this script keeps:
#   - a scratch LYCAON_CONFIG_DIR, never the shared paintedwolf-dev store
#   - a port probed free at run time, never :8787
#   - only the pid this script started is ever signalled
#
# Required environment:
#   SCANSTRESS_SIDECAR    built sidecar binary (go build -o … ./cmd/lycaon)
#   SCANSTRESS_MODULE     module root the sidecar runs from (…/lycaon)
#   SCANSTRESS_CANDIDATE  maintained engine artifact directory
#   SCANSTRESS_WORK       scratch directory for the store, corpus, and logs
set -uo pipefail
BIN="${SCANSTRESS_SIDECAR:?set SCANSTRESS_SIDECAR to the built sidecar}"
MODULE="${SCANSTRESS_MODULE:?set SCANSTRESS_MODULE to the module root}"
CANDIDATE="${SCANSTRESS_CANDIDATE:?set SCANSTRESS_CANDIDATE to the engine artifact directory}"
WORK="${SCANSTRESS_WORK:?set SCANSTRESS_WORK to a scratch directory}"

CFG="${WORK}/sidecar-cfg"
PROJECT="${WORK}/sidecar-project"
LOG="${WORK}/sidecar.log"
OUT="${WORK}/sidecar-shutdown.txt"

say() { printf '%s\n' "$*" | tee -a "$OUT"; }

rm -rf "$CFG" "$PROJECT" "$LOG" "$OUT"
mkdir -p "$CFG" "$PROJECT"

# A corpus large enough that the scan is still running when the host is stopped.
python3 - "$PROJECT" <<'PYEOF'
import os, sys
root = sys.argv[1]
for i in range(240):
    ext = ["py","js","ts","vue"][i % 4]
    body = []
    for v in range(3):
        if ext == "py":
            body.append(f"import os, subprocess, hashlib\ndef h_{i}_{v}(u):\n    d = hashlib.md5(u.encode()).hexdigest()\n    r = eval(u)\n    subprocess.call('echo ' + u, shell=True)\n    os.system('ls ' + u)\n    return d, r\n")
        elif ext in ("js","ts"):
            body.append(f"function f_{i}_{v}(req){{ const raw = req.query.v; document.getElementById('o').innerHTML = raw; eval(raw); return raw.length; }}\n")
        else:
            body.append(f"<template><div/></template>\n<script setup lang=\"ts\">\nconst r_{i}_{v}: string = location.hash;\neval(r_{i}_{v});\n</script>\n")
    open(os.path.join(root, f"mod_{i:03d}.{ext}"), "w").write("".join(body))
PYEOF
say "corpus files: $(ls "$PROJECT" | wc -l | tr -d ' ')"

# Probe a free loopback port rather than reusing a habitual one.
PORT=""
for candidate in $(seq 8860 8900); do
  if [ -z "$(lsof -ti:"${candidate}" -sTCP:LISTEN 2>/dev/null)" ]; then PORT="${candidate}"; break; fi
done
[ -n "$PORT" ] || { say "no free port in 8860-8900"; exit 1; }
ADDR="127.0.0.1:${PORT}"
TOKEN="scanstress-$(date +%s)"
say "sidecar addr=${ADDR} config=${CFG}"

start_sidecar() {
  (
    cd "$MODULE" || exit 1
    export LYCAON_ADDR="$ADDR" LYCAON_API_TOKEN="$TOKEN" LYCAON_CONFIG_DIR="$CFG" \
           LYCAON_LLM_MOCK=1 LYCAON_OPENGREP_CANDIDATE="$CANDIDATE"
    exec "$BIN" serve --db "${CFG}/store.db"
  ) >>"$LOG" 2>&1 &
  SIDECAR_PID=$!
}

wait_health() {
  for _ in $(seq 1 120); do
    if curl -fsS "http://${ADDR}/health" >/dev/null 2>&1; then return 0; fi
    if ! kill -0 "$SIDECAR_PID" 2>/dev/null; then say "sidecar exited early; log tail:"; tail -20 "$LOG" | tee -a "$OUT"; return 1; fi
    sleep 1
  done
  return 1
}

api() { curl -fsS -H "Authorization: Bearer ${TOKEN}" "$@"; }

# Descendants of a pid, by parent link only. Nothing is matched by name.
descendants() {
  local out="" frontier="$1" next kids
  while [ -n "$frontier" ]; do
    next=""
    for p in $frontier; do
      kids="$(pgrep -P "$p" 2>/dev/null | tr '\n' ' ')"
      next="${next} ${kids}"
      out="${out} ${kids}"
    done
    frontier="$(printf '%s' "$next" | tr -s ' ')"
  done
  printf '%s' "$out" | tr -s ' '
}

start_sidecar
say "sidecar pid=${SIDECAR_PID}"
wait_health || { kill "$SIDECAR_PID" 2>/dev/null; exit 1; }
say "health ok"

SCAN_JSON="$(api -X POST "http://${ADDR}/v1/scans" -H 'Content-Type: application/json' \
  -d "{\"project_dir\":\"${PROJECT}\",\"categories\":[\"sast\"],\"scanner_id\":\"lycaon-sast\"}")" || {
  say "create scan failed"; tail -20 "$LOG" | tee -a "$OUT"; kill "$SIDECAR_PID" 2>/dev/null; exit 1; }
SCAN_ID="$(printf '%s' "$SCAN_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
say "scan id=${SCAN_ID}"

# Wait until the engine is actually executing under this sidecar.
ENGINE_PIDS=""
for _ in $(seq 1 300); do
  ENGINE_PIDS=""
  for p in $(descendants "$SIDECAR_PID"); do
    if ps -p "$p" -o command= 2>/dev/null | grep -q opengrep; then ENGINE_PIDS="${ENGINE_PIDS} ${p}"; fi
  done
  ENGINE_PIDS="$(printf '%s' "$ENGINE_PIDS" | tr -s ' ' | sed 's/^ //')"
  [ -n "$ENGINE_PIDS" ] && break
  sleep 1
done
[ -n "$ENGINE_PIDS" ] || { say "engine never started"; tail -30 "$LOG" | tee -a "$OUT"; kill "$SIDECAR_PID" 2>/dev/null; exit 1; }
say "engine pids:${ENGINE_PIDS}"
sleep 5

say "status before shutdown: $(api "http://${ADDR}/v1/scans/${SCAN_ID}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status"), d.get("failure_code",""), repr(d.get("error","")))')"

KILL_AT=$(python3 -c 'import time; print(time.time())')
say "sending SIGTERM to sidecar ${SIDECAR_PID} (this script's own process only)"
kill -TERM "$SIDECAR_PID"
wait "$SIDECAR_PID" 2>/dev/null
EXIT_AT=$(python3 -c 'import time; print(time.time())')
say "sidecar exited after $(python3 -c "print(round(${EXIT_AT}-${KILL_AT},2))")s"

sleep 2
ORPHANS=""
for p in $ENGINE_PIDS; do
  if ps -p "$p" -o command= 2>/dev/null | grep -q opengrep; then ORPHANS="${ORPHANS} ${p}"; fi
done
say "engine orphans after shutdown:${ORPHANS:- none}"
for p in $ORPHANS; do kill -9 "$p" 2>/dev/null; done

# Restart on the same store and read the durable status back.
start_sidecar
say "restarted sidecar pid=${SIDECAR_PID}"
wait_health || { kill "$SIDECAR_PID" 2>/dev/null; exit 1; }
read_status() { api "http://${ADDR}/v1/scans/${SCAN_ID}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status"), d.get("failure_code",""), repr(d.get("error","")), d.get("coverage_status",""))'; }
say "status immediately after restart: $(read_status)"

# The claim lease is 45s; recovery cannot be observed before it expires.
for i in $(seq 1 24); do
  sleep 5
  CUR="$(read_status)"
  say "t+$((i*5))s: ${CUR}"
  case "$CUR" in running*|pending*) ;; *) break ;; esac
done

kill -TERM "$SIDECAR_PID" 2>/dev/null
wait "$SIDECAR_PID" 2>/dev/null
say "done"
