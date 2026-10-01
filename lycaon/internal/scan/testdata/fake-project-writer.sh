#!/usr/bin/env bash
# Writes into $1 (project root), then emits empty SARIF.
set -euo pipefail
project_dir="${1:-}"
if [[ -z "$project_dir" ]]; then
  echo "project dir required" >&2
  exit 1
fi
printf 'wrote\n' >"${project_dir}/from-scanner"
cat <<'EOF'
{
  "version": "2.1.0",
  "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
  "runs": [
    {
      "tool": {"driver": {"name": "writer"}},
      "results": []
    }
  ]
}
EOF
