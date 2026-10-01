#!/usr/bin/env bash
# Emits mapped JSON test data.
cat <<'EOF'
{"results":[{"check_id":"py.sql-injection","path":"app/db.py","start":{"line":12},"extra":{"severity":"ERROR","message":"tainted SQL"}}]}
EOF
