#!/usr/bin/env bash
# testdata fake external scanner — emits minimal SARIF 2.1 on stdout.
cat <<'EOF'
{
  "version": "2.1.0",
  "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
  "runs": [
    {
      "tool": {"driver": {"name": "communitytool", "semanticVersion": "1.2.3"}},
      "results": [
        {
          "ruleId": "hardcoded-password",
          "level": "error",
          "message": {"text": "hardcoded password detected"},
          "locations": [
            {
              "physicalLocation": {
                "artifactLocation": {"uri": "src/auth.go"},
                "region": {"startLine": 21}
              }
            }
          ]
        }
      ]
    }
  ]
}
EOF
