#!/usr/bin/env bash
# Emits a failing secret-scanner report containing its matched value.
cat <<'EOF'
{
  "version": "2.1.0",
  "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
  "runs": [
    {
      "tool": {"driver": {"name": "fakeleaks", "semanticVersion": "1.0.0"}},
      "results": [
        {
          "ruleId": "github-pat",
          "message": {"text": "github-pat has detected secret for file creds.yaml"},
          "locations": [
            {
              "physicalLocation": {
                "artifactLocation": {"uri": "creds.yaml"},
                "region": {
                  "startLine": 1,
                  "snippet": {"text": "ghp_A9fK2mQ7zX4bR1nT6yW8pL3vC5dH0jS2gU7e"}
                }
              }
            }
          ]
        }
      ]
    }
  ]
}
EOF
echo "fakeleaks: leaked ghp_A9fK2mQ7zX4bR1nT6yW8pL3vC5dH0jS2gU7e in creds.yaml" >&2
exit 7
