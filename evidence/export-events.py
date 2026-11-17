"""Export a debug session's message events for replay-row-order.ts.

usage: python3 export-events.py <debug-session-dir> <out.json>

Message text and tool output are reduced to a marker; only structure is kept.
"""
import collections, json, os, sys

src, out = sys.argv[1], sys.argv[2]
per = collections.defaultdict(list)
for line in open(os.path.join(src, "sse-events.jsonl"), errors="replace"):
    try:
        event = json.loads(line)
    except ValueError:
        continue
    if event.get("topic") != "message":
        continue
    data = event["envelope"].get("data") or {}
    message = data.get("message")
    if not isinstance(message, dict):
        continue
    if isinstance(message.get("content"), str):
        message["content"] = "x" if message["content"].strip() else ""
    message.pop("reasoning", None)
    result = message.get("tool_result")
    if isinstance(result, dict):
        for key, value in list(result.items()):
            if isinstance(value, str) and len(value) > 200:
                result[key] = value[:40]
        for preview in result.get("file_edit_previews") or []:
            for key, value in list(preview.items()):
                if isinstance(value, str) and len(value) > 200:
                    preview[key] = value[:40]
    per[event.get("session_id") or ""].append({"op": data.get("op"), "message": message})
name = os.path.basename(os.path.normpath(src))
json.dump({f"{name}/{sid[:8]}": events for sid, events in per.items()}, open(out, "w"))
print({f"{name}/{sid[:8]}": len(events) for sid, events in per.items()})
