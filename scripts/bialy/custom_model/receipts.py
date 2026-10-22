"""Read-only admission against captured requests and actual host receipts.

This establishes execution acceptance, not semantic quality. It cannot certify
counterfactual execution or turn a later recovery into a same-state preference.

The training input is the request the host rendered for the student's configured
serving provider, decoded into the chat-template input that provider's server
builds. The teacher's exact dispatch is kept as provenance by digest.
"""
from datetime import datetime
import hashlib
import json
import sqlite3
from pathlib import Path
from urllib.parse import quote

from command_contract import admit_program
from common import digest


def openai_chat(wire):
    """llama.cpp's server with --jinja: OpenAI chat messages with tool-call
    arguments deserialized before the checkpoint's own chat template runs."""
    supported = {"model", "messages", "tools", "stream", "stream_options", "temperature", "top_p",
                 "max_tokens", "max_completion_tokens", "reasoning_effort", "tool_choice", "parallel_tool_calls"}
    if set(wire) - supported or not isinstance(wire.get("messages"), list):
        raise ValueError("unsupported student request shape")
    controls = {k: v for k, v in wire.items() if k not in {"model", "messages", "tools", "stream", "stream_options"}}
    if controls.get("tool_choice") not in (None, "auto") or controls.get("parallel_tool_calls") is False or controls.get("reasoning_effort") not in (None, "none", "minimal"):
        raise ValueError("request control needs a model-specific renderer")
    messages = []
    for m in wire["messages"]:
        content = m.get("content")
        if content is not None and not isinstance(content, str):
            raise ValueError("unsupported multimodal student request")
        if set(m) - {"role", "content", "tool_calls", "tool_call_id", "reasoning_content"} or m.get("reasoning_content"):
            raise ValueError("unsupported student message fields")
        out = {"role": m["role"], "content": content or ""}
        if m.get("tool_calls"):
            out["tool_calls"] = [{"id": c["id"], "type": "function", "function": {
                "name": c["function"]["name"], "arguments": _object(c["function"].get("arguments"))}} for c in m["tool_calls"]]
        if m.get("tool_call_id"):
            out["tool_call_id"] = m["tool_call_id"]
        messages.append(out)
    return messages, wire.get("tools", []), controls


def _object(arguments):
    value = json.loads(arguments) if isinstance(arguments, str) else arguments
    if not isinstance(value, dict):
        raise ValueError("tool-call arguments are not a JSON object")
    return value


# Serving kinds whose server applies the checkpoint's own chat template, so the
# trained token sequence is the served one. Ollama renders with its own Go
# templates, whose parity with the checkpoint template is not established.
SERVING_PROTOCOLS = {"openai-compatible": openai_chat}


def response(value):
    calls = value.get("tool_calls") or []
    if any(c.get("args_ref") or c.get("args_truncated") or c.get("args_malformed") or c.get("extra_content") for c in calls):
        raise ValueError("unsupported response metadata")
    return {"role": "assistant", "text": value["content"], "tool_calls": [
        {"id": c["id"], "name": c["name"], "arguments": c["args"]} for c in calls]}


def _instant(value):
    """Go RFC 3339 timestamps carry up to nanoseconds; Python parses microseconds."""
    head, _, rest = value.rstrip("Z").partition(".")
    fraction = (rest + "000000")[:6] if rest else "000000"
    return datetime.fromisoformat(head + "." + fraction + "+00:00")


def args_digest(arguments):
    # Go canonical JSON escapes HTML and U+2028/U+2029. Float encoding differs
    # across runtimes; exclude it instead of accepting an approximate digest.
    def has_float(v):
        return isinstance(v, float) or isinstance(v, list) and any(map(has_float, v)) or isinstance(v, dict) and any(map(has_float, v.values()))
    if has_float(arguments):
        raise ValueError("floating argument canonicalization unsupported")
    encoded = json.dumps(arguments, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    for char, escaped in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"), (" ", "\\u2028"), (" ", "\\u2029")):
        encoded = encoded.replace(char, escaped)
    return hashlib.sha256(encoded.encode()).hexdigest()


def accepted_row(record, db, identity, catalog_sha256, commits):
    """commits maps (session, message) to when the host committed that message as
    a turn's visible result; it is the acceptance evidence for a tool-free reply."""
    if record.get("schema") != "pw-training-turn/1":
        raise ValueError("unsupported capture schema")
    if record.get("exclusion_reason"):
        # The host's reason (student_cannot_be_served, ambiguous_dispatch, ...) is the count key.
        raise ValueError("host capture excluded: " + str(record["exclusion_reason"]))
    if record.get("dispatches") != 1 or not record.get("settled"):
        raise ValueError("excluded host capture")
    if record["epoch"] != identity["epoch"]:
        raise ValueError("foreign collection epoch")
    student = (record.get("student_provider"), record.get("student_kind"), record.get("student_model"))
    if student != (identity["student_provider"], identity["student_kind"], identity["student_model"]):
        raise ValueError("capture rendered for another student")
    decode = SERVING_PROTOCOLS.get(record["student_kind"])
    if decode is None:
        raise ValueError("serving kind without checkpoint-template parity: " + str(record["student_kind"]))
    messages, tools, controls = decode(record["student_request"])
    output = db.execute("SELECT * FROM model_outputs WHERE id=? AND session_id=? AND message_id=?",
                        (record["output_id"], record["session_id"], record["message_id"])).fetchone()
    if output is None or output["scripted"] or not output["settled_at"]:
        raise ValueError("missing settled model output")
    target = response(record["response"])
    receipt_ids = []
    for call in target["tool_calls"]:
        receipt = db.execute("SELECT * FROM invocation_receipts WHERE session_id=? AND assistant_message_id=? AND tool_call_id=?",
                             (record["session_id"], record["message_id"], call["id"])).fetchone()
        digest_ = args_digest(call["arguments"])
        if (receipt is None or receipt["tool_name"] != call["name"] or receipt["status"] != "completed"
                or not receipt["invoked"] or not receipt["settled_at"] or receipt["args_digest"] != digest_):
            raise ValueError("missing exact successful invocation receipt")
        receipt_ids.append(receipt["id"])
        if identity["tool_surface"] == "command-v1":
            receipt_ids.extend(admit_program(db, receipt, digest_))
    if not target["tool_calls"]:
        committed = commits.get((record["session_id"], record["message_id"]))
        if committed is None:
            raise ValueError("tool-free reply the host did not commit")
        # The committed message is the one its latest output settled before the commit.
        settled = db.execute("SELECT id, settled_at FROM model_outputs WHERE session_id=? AND message_id=? AND settled_at IS NOT NULL",
                             (record["session_id"], record["message_id"])).fetchall()
        before = [r for r in settled if _instant(r["settled_at"]) <= _instant(committed)]
        if not before or max(before, key=lambda r: _instant(r["settled_at"]))["id"] != output["id"]:
            raise ValueError("committed message came from another output")
    capture = {"schema": "pw-model-request-capture/2", "request_id": record["call_id"],
               "session_id": record["session_id"], "message_id": record["message_id"],
               "host_revision": identity["host_commit"], "epoch": identity["epoch"],
               "tool_surface": identity["tool_surface"], "catalog_sha256": catalog_sha256,
               "student_provider": student[0], "student_kind": student[1], "student_model": student[2],
               "dispatch_sha256": digest(record["request"]),
               "state_sha256": digest({"request": record["student_request"], "turn_attempt_id": output["turn_attempt_id"], "output_id": output["id"]}),
               "messages": messages, "tools": tools, "controls": controls}
    capture["sha256"] = digest(capture)
    report = {k: capture[k] for k in ("state_sha256", "host_revision", "epoch", "tool_surface", "catalog_sha256")}
    report.update(schema="pw-training-validation/1", validator="paintedwolf-host-training-validation/1",
                  receipt_id=output["id"], invocation_receipt_ids=receipt_ids,
                  request_sha256=capture["sha256"], response_sha256=digest(target), kind="accepted_response",
                  response_origin="model", outcome="accepted",
                  acceptance="turn_committed" if not target["tool_calls"] else "invocation_receipts")
    return {"schema": "pw-custom-sft/1", "session": record["session_id"], "message_id": record["message_id"],
            "prompt": capture["messages"], "target": target, "request_capture": capture, "validation": report}


def open_store(path):
    # mode=ro still observes a present WAL; caller binds all inputs before/after.
    db = sqlite3.connect("file:" + quote(str(Path(path).resolve()), safe="/") + "?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    return db
