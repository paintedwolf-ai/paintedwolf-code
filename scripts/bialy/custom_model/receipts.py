"""Read-only admission against captured requests and actual host receipts.

This establishes execution acceptance, not semantic quality. It cannot certify
counterfactual execution or turn a later recovery into a same-state preference.
"""
import json
import sqlite3
from pathlib import Path
from urllib.parse import quote

from common import digest


def response(value):
    calls = value.get("tool_calls") or []
    if any(c.get("args_ref") or c.get("args_truncated") or c.get("args_malformed") or c.get("extra_content") for c in calls):
        raise ValueError("unsupported response metadata")
    return {"role": "assistant", "text": value["content"], "tool_calls": [
        {"id": c["id"], "name": c["name"], "arguments": c["args"]} for c in calls]}


def accepted_row(record, db, identity, catalog_sha256):
    if record.get("schema") != "pw-training-turn/1":
        raise ValueError("unsupported capture schema")
    if record.get("exclusion_reason"):
        # The host's reason (unsupported_response, ambiguous_dispatch, ...) is the count key.
        raise ValueError("host capture excluded: " + str(record["exclusion_reason"]))
    if record.get("dispatches") != 1 or not record.get("settled"):
        raise ValueError("excluded host capture")
    if record["epoch"] != identity["epoch"]:
        raise ValueError("foreign collection epoch")
    wire = record["request"]
    supported = {"model", "messages", "tools", "stream", "stream_options", "temperature", "top_p", "max_tokens", "max_completion_tokens", "reasoning_effort", "tool_choice", "parallel_tool_calls"}
    if set(wire) - supported or not isinstance(wire.get("messages"), list):
        raise ValueError("unsupported provider request shape")
    # Tool-disabled requests need their control retained by the tokenizer path.
    controls = {k: v for k, v in wire.items() if k not in {"model", "messages", "tools", "stream", "stream_options"}}
    if controls.get("tool_choice") not in (None, "auto") or controls.get("parallel_tool_calls") is False or controls.get("reasoning_effort") not in (None, "none", "minimal"):
        raise ValueError("request control needs a model-specific renderer")
    output = db.execute("SELECT * FROM model_outputs WHERE id=? AND session_id=? AND message_id=?",
                        (record["output_id"], record["session_id"], record["message_id"])).fetchone()
    if output is None or output["scripted"] or not output["settled_at"]:
        raise ValueError("missing settled model output")
    target = response(record["response"])
    receipt_ids = []
    for call in target["tool_calls"]:
        receipt = db.execute("SELECT * FROM invocation_receipts WHERE session_id=? AND assistant_message_id=? AND tool_call_id=?",
                             (record["session_id"], record["message_id"], call["id"])).fetchone()
        # Go canonical JSON escapes HTML and U+2028/U+2029. Float encoding differs
        # across runtimes; exclude it instead of accepting an approximate digest.
        def has_float(v):
            return isinstance(v, float) or isinstance(v, list) and any(map(has_float, v)) or isinstance(v, dict) and any(map(has_float, v.values()))
        if has_float(call["arguments"]):
            raise ValueError("floating argument canonicalization unsupported")
        import hashlib
        encoded = json.dumps(call["arguments"], sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        for char, escaped in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
            encoded = encoded.replace(char, escaped)
        if (receipt is None or receipt["tool_name"] != call["name"] or receipt["status"] != "completed"
                or not receipt["invoked"] or not receipt["settled_at"]
                or receipt["args_digest"] != hashlib.sha256(encoded.encode()).hexdigest()):
            raise ValueError("missing exact successful invocation receipt")
        receipt_ids.append(receipt["id"])
    if not target["tool_calls"]:
        # Final prose requires a dedicated closeout validator; model settlement
        # by itself does not establish publication or evidence correctness.
        raise ValueError("prose closeout requires publication validation")
    capture = {"schema": "pw-model-request-capture/2", "request_id": record["call_id"],
               "session_id": record["session_id"], "message_id": record["message_id"],
               "host_revision": identity["host_commit"], "epoch": identity["epoch"],
               "tool_surface": identity["tool_surface"], "catalog_sha256": catalog_sha256,
               "state_sha256": digest({"request": wire, "turn_attempt_id": output["turn_attempt_id"], "output_id": output["id"]}),
               "messages": wire["messages"], "tools": wire.get("tools", []), "controls": controls}
    capture["sha256"] = digest(capture)
    report = {k: capture[k] for k in ("state_sha256", "host_revision", "epoch", "tool_surface", "catalog_sha256")}
    report.update(schema="pw-training-validation/1", validator="paintedwolf-host-training-validation/1",
                  receipt_id=output["id"], invocation_receipt_ids=receipt_ids,
                  request_sha256=capture["sha256"], response_sha256=digest(target), kind="accepted_response",
                  response_origin="model", outcome="accepted")
    return {"schema": "pw-custom-sft/1", "session": record["session_id"], "message_id": record["message_id"],
            "prompt": capture["messages"], "target": target, "request_capture": capture, "validation": report}


def open_store(path):
    # mode=ro still observes a present WAL; caller binds all inputs before/after.
    db = sqlite3.connect("file:" + quote(str(Path(path).resolve()), safe="/") + "?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    return db
