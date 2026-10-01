#!/usr/bin/env bash
# testdata fake external scanner — emits lycaon_findings_json format 1 on stdout.
cat <<'EOF'
{"format":1,"findings":[{"rule_id":"fixture-rule","level":"medium","message":"fixture finding","locations":[{"uri":"src/app.py","start_line":7}]}]}
EOF
