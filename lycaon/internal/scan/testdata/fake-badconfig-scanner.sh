#!/usr/bin/env bash
# Emits an empty report and exits with a configuration error.
cat <<'EOF'
{
  "version": "2.1.0",
  "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
  "runs": [
    {
      "tool": {"driver": {"name": "communitytool", "semanticVersion": "1.2.3"}},
      "invocations": [
        {
          "executionSuccessful": true,
          "toolExecutionNotifications": [
            {"level": "error", "message": {"text": "unable to find a config"}}
          ]
        }
      ],
      "results": []
    }
  ]
}
EOF
exit 7
