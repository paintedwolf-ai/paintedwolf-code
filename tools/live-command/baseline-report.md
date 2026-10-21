# Portable command environment live multi-model report (73fb670a28)

- Generated at: `2026-10-21T15:12:49Z`
- Tasks per model: `10`
- Attempts per task: `1` (all attempts retained in chronological order)

## Model summary

| Provider | Model | Passed | Total | Pass rate | Native gaps | Refusals |
|---|---|---:|---:|---:|---:|---:|
| `together-ai-1` | `zai-org/GLM-5.3-Flash` | 8 | 10 | 80.0% | 10 | 9 |
| `ollama-2` | `ministral-3:3b` | 1 | 10 | 10.0% | 29 | 21 |
| `together-ai-1` | `Qwen/Qwen3.5-9B` | 5 | 10 | 50.0% | 12 | 14 |

## `together-ai-1` / `zai-org/GLM-5.3-Flash`

### Task attempts

| # | Task | Attempt | Status | Elapsed (s) | Programs | Executions | Grade | Notes |
|---:|---|---:|---|---:|---:|---:|---|---|
| 1 | `L01-go-files` | 1 | `idle` | 10.61 | 1 | 1 | **PASS** | ok |
| 2 | `L02-tests` | 1 | `idle` | 38.15 | 5 | 5 | **PASS** | ok |
| 3 | `L03-todo-notes` | 1 | `idle` | 39.45 | 1 | 5 | **FAIL** | notes.txt missing tool.go; notes.txt missing legacy.js; notes.txt missing broken.go; notes.txt missing calc.go; notes.txt missing main.go |
| 4 | `L04-rename-key` | 1 | `idle` | 21.48 | 3 | 9 | **PASS** | ok |
| 5 | `L05-bg-job` | 1 | `idle` | 58.22 | 3 | 12 | **PASS** | ok |
| 6 | `L06-find-size` | 1 | `idle` | 13.84 | 1 | 4 | **PASS** | ok |
| 7 | `L07-log-stats` | 1 | `idle` | 48.39 | 3 | 19 | **PASS** | ok |
| 8 | `L08-sed-edit` | 1 | `idle` | 42.30 | 4 | 7 | **PASS** | ok |
| 9 | `L09-git` | 1 | `idle` | 33.40 | 1 | 5 | **PASS** | ok |
| 10 | `L10-curl` | 1 | `idle` | 17.12 | 1 | 1 | **FAIL** | reply missing status.json fields (sanity, 2.4.1) |

### Native gaps (`native_gap` facts)

- `jq`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 2
- `git`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 2
- `echo`: `COMMAND_OPTION_UNSUPPORTED` (`---`) — 1
- `echo`: `COMMAND_OPTION_UNSUPPORTED` (`--- after ---`) — 1
- `echo`: `COMMAND_OPTION_UNSUPPORTED` (`--- LOG ---`) — 1
- `git log`: `COMMAND_OPTION_UNSUPPORTED` (`--stat`) — 1
- `echo`: `COMMAND_OPTION_UNSUPPORTED` (`--- DIFF ---`) — 1
- `curl`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 1

### Refusals and stop codes

- `COMMAND_OPERAND_INVALID` (`ps aux | grep '[s]leep 120'`) — 2
- `COMMAND_OPERAND_INVALID` (`ps aux`) — 2
- `COMMAND_OPERAND_INVALID` (`find . -name '*.go' -type f -print0 | xargs -0 wc -l | sort -n`) — 1
- `TOOL_OWNER_INTERRUPTED` (`sleep 120 &`) — 1
- `SANDBOX_LOOPBACK_CONNECT_DENIED` (`curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:57637/`) — 1
- `TOOL_OWNER_FAILED` (`oar_or_stop`) — 1
- `TOOL_ARGS_INVALID` (`oar_or_stop`) — 1

## `ollama-2` / `ministral-3:3b`

### Task attempts

| # | Task | Attempt | Status | Elapsed (s) | Programs | Executions | Grade | Notes |
|---:|---|---:|---|---:|---:|---:|---|---|
| 1 | `L01-go-files` | 1 | `idle` | 21.41 | 0 | 0 | **FAIL** | model did not invoke the command tool |
| 2 | `L02-tests` | 1 | `idle` | 51.94 | 1 | 2 | **FAIL** | reply did not explain TestDouble failure and n*2+1 cause |
| 3 | `L03-todo-notes` | 1 | `timeout` | 120.57 | 6 | 22 | **FAIL** | notes.txt was not created on disk |
| 4 | `L04-rename-key` | 1 | `timeout` | 121.13 | 18 | 28 | **FAIL** | config.json unexpected content: {'name': 'sanity', 'timeout': 30, 'verbose': False, 'x': 42} |
| 5 | `L05-bg-job` | 1 | `idle` | 44.11 | 16 | 36 | **PASS** | ok |
| 6 | `L06-find-size` | 1 | `idle` | 13.66 | 2 | 6 | **FAIL** | reply must list big.bin and medium.dat and exclude small.dat |
| 7 | `L07-log-stats` | 1 | `idle` | 80.54 | 23 | 133 | **FAIL** | reply missing expected log stat count 66; reply missing expected log stat count 33; reply missing expected log stat count 21; reply missing expected log stat count 26; reply missing expected log stat count 22 |
| 8 | `L08-sed-edit` | 1 | `timeout` | 121.03 | 20 | 136 | **FAIL** | docs/nested/list.txt lines=['end'], expected ['ALPHA', 'beta', 'end'] |
| 9 | `L09-git` | 1 | `idle` | 15.12 | 3 | 6 | **FAIL** | reply missing git status/diff summary for README.txt |
| 10 | `L10-curl` | 1 | `idle` | 16.62 | 4 | 6 | **FAIL** | reply missing status.json fields (sanity, 2.4.1) |

### Native gaps (`native_gap` facts)

- `jq`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 20
- `curl`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 4
- `jobs`: `COMMAND_OPTION_UNSUPPORTED` (`-l`) — 2
- `git`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 2
- `git log`: `COMMAND_OPTION_UNSUPPORTED` (`--all`) — 1

### Refusals and stop codes

- `COMMAND_OPERAND_INVALID` (`ps aux`) — 7
- `COMMAND_OPERAND_INVALID` (`ps aux | grep -i 'sleep'`) — 3
- `COMMAND_OPERAND_INVALID` (`ps aux | grep -E 'sleep|^4198'`) — 2
- `COMMAND_OPERAND_INVALID` (`find . -type f -exec grep -l 'TODO' {} \; | xargs -I {} sh -c 'grep -n "TODO" {}`) — 1
- `TOOL_OWNER_INTERRUPTED` (`xargs -I {} sh -c 'grep -n "TODO" {} >> notes.txt'`) — 1
- `TOOL_OWNER_FAILED` (`sleep 120 &`) — 1
- `COMMAND_JOB_UNKNOWN` (`kill $(pgrep sleep)`) — 1
- `COMMAND_OPERAND_INVALID` (`ps aux | grep sleep`) — 1
- `COMMAND_JOB_UNKNOWN` (`kill -9 $(pgrep -f sleep)`) — 1
- `COMMAND_OPERAND_INVALID` (`ps aux | grep -E 'sleep'`) — 1
- `SANDBOX_LOOPBACK_CONNECT_DENIED` (`curl -s http://127.0.0.1:57637/`) — 1
- `SANDBOX_LOOPBACK_CONNECT_DENIED` (`curl -s http://127.0.0.1:57637/status.json`) — 1

## `together-ai-1` / `Qwen/Qwen3.5-9B`

### Task attempts

| # | Task | Attempt | Status | Elapsed (s) | Programs | Executions | Grade | Notes |
|---:|---|---:|---|---:|---:|---:|---|---|
| 1 | `L01-go-files` | 1 | `idle` | 12.14 | 0 | 0 | **FAIL** | model did not invoke the command tool; reply missing cmd/tool/tool.go (28 lines); reply missing total Go file count (6) |
| 2 | `L02-tests` | 1 | `idle` | 45.69 | 1 | 1 | **FAIL** | reply did not explain TestDouble failure and n*2+1 cause |
| 3 | `L03-todo-notes` | 1 | `idle` | 33.51 | 2 | 10 | **PASS** | ok |
| 4 | `L04-rename-key` | 1 | `idle` | 71.45 | 3 | 12 | **PASS** | ok |
| 5 | `L05-bg-job` | 1 | `idle` | 113.00 | 8 | 15 | **PASS** | ok |
| 6 | `L06-find-size` | 1 | `idle` | 47.29 | 2 | 14 | **FAIL** | reply must list big.bin and medium.dat and exclude small.dat |
| 7 | `L07-log-stats` | 1 | `idle` | 50.23 | 2 | 2 | **FAIL** | reply missing expected log stat count 22 |
| 8 | `L08-sed-edit` | 1 | `idle` | 39.74 | 4 | 7 | **PASS** | ok |
| 9 | `L09-git` | 1 | `idle` | 31.86 | 1 | 1 | **PASS** | ok |
| 10 | `L10-curl` | 1 | `idle` | 57.83 | 9 | 13 | **FAIL** | reply missing status.json fields (sanity, 2.4.1) |

### Native gaps (`native_gap` facts)

- `curl`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 9
- `jq`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 2
- `git`: `COMMAND_NATIVE_FORM_UNSUPPORTED` — 1

### Refusals and stop codes

- `COMMAND_OPERAND_INVALID` (`ps aux | grep sleep`) — 2
- `COMMAND_OPERAND_INVALID` (`ps aux`) — 2
- `SANDBOX_DIRECT_IP_DENIED` (`curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 http://127.0.0.1:5763`) — 2
- `TOOL_ARGS_INVALID` (`oar_or_stop`) — 2
- `TOOL_OWNER_FAILED` (`sleep 120 &`) — 1
- `TOOL_OWNER_INTERRUPTED` (`sleep 120 &`) — 1
- `SANDBOX_DIRECT_IP_DENIED` (`curl -s --connect-timeout 5 http://127.0.0.1:57637/`) — 1
- `SANDBOX_DIRECT_IP_DENIED` (`curl -s --connect-timeout 5 http://127.0.0.1:57637/status.json`) — 1
- `SURFACE_NOTE_UNGROUNDED` (`oar_or_stop`) — 1
- `TOOL_OWNER_FAILED` (`oar_or_stop`) — 1
