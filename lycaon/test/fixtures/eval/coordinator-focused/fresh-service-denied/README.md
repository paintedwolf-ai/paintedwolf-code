# Balance handoff
client.py refreshes receipt.json using endpoint.json and events.json. Its --minimum option keeps balances at or above the requested value after summing signed account events. If service access is denied, use --offline to read fallback.json instead. Leave client.py, events.json, fallback.json and endpoint.json unchanged.
Keep the supplied tests and this README unchanged. Use regular project files, Python standard library only, and no downloads or dependencies.
