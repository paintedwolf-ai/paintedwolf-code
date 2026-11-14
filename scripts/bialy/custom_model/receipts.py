"""Training candidates built from captured turns and one host episode bundle.

A candidate states facts and makes no admission decision: the student request decoded
into the chat-template input its serving provider builds, the response, and for each
call the operations the host recorded. The factory's training-row admission decides
eligibility from these facts. The teacher's exact dispatch is kept as provenance by
digest. Every host fact comes from the bundle; nothing here names a host tool or reads
host storage.
"""
import hashlib
import json

from common import digest


def openai_chat(wire):
    """Decode structured chat requests for the artifact's exact template."""
    supported = {"model", "messages", "tools", "stream", "stream_options", "temperature", "top_p",
                 "max_tokens", "max_completion_tokens", "reasoning_effort", "tool_choice", "parallel_tool_calls"}
    if set(wire) - supported or not isinstance(wire.get("messages"), list):
        raise ValueError("unsupported student request shape")
    controls = {k: v for k, v in wire.items() if k not in {"model", "messages", "tools", "stream", "stream_options"}}
    if controls.get("tool_choice") not in (None, "auto") or controls.get("parallel_tool_calls") is False or controls.get("reasoning_effort") != "none":
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


# Serving protocols must apply the artifact's exact chat template.
SERVING_PROTOCOLS = {"openai-compatible": openai_chat}


def response(value):
    calls = value.get("tool_calls") or []
    if any(c.get("args_ref") or c.get("args_truncated") or c.get("args_malformed") or c.get("extra_content") for c in calls):
        raise ValueError("unsupported response metadata")
    return {"role": "assistant", "text": value["content"], "tool_calls": [
        {"id": c["id"], "name": c["name"], "arguments": c["args"]} for c in calls]}


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


def call_facts(bundle, record, call, offered):
    """What the host recorded for one call: its operation and every child it ran."""
    digest_ = args_digest(call["arguments"])
    operation = bundle.calls.get((record["session_id"], call["id"]))
    matched = (operation is not None and operation.get("message_id") == record["message_id"]
               and operation.get("tool") == call["name"] and operation.get("args_sha256") == digest_)
    facts = {"call_id": call["id"], "tool": call["name"], "offered": call["name"] in offered, "recorded": matched}
    if not matched:
        return facts
    family = [operation, *bundle.children.get(operation["id"], [])]
    prior_unknown = any(prior.get("tool") == operation["tool"] and prior.get("args_sha256") == digest_
                        and any(item["effect_state"] == "unknown" for item in [prior, *bundle.children.get(prior["id"], [])])
                        for prior in bundle.earlier(operation))
    facts.update(operation_ids=[item["id"] for item in family], invoked=operation["invoked"],
                 settlement="settled" if all(item["settlement"] == "settled" for item in family) else "unsettled",
                 semantic_outcome=operation["semantic_outcome"],
                 effect_state="unknown" if any(item["effect_state"] == "unknown" for item in family) else
                              "known" if any(item["effect_state"] == "known" for item in family) else "none",
                 failure_code=operation.get("failure_code", ""), source_verdict=operation.get("source_verdict", ""),
                 source_revision=operation.get("source_revision", ""), replays_unknown_effects=prior_unknown,
                 started_at=operation["started_at"])
    return facts


def candidate(record, bundle, identity, catalog_sha256):
    """One capture line as a training candidate. A capture the host already excluded becomes
    a stub carrying the host's reason; a capture no candidate can be built from raises."""
    if record.get("schema") != "pw-training-turn/1":
        raise ValueError("unsupported capture schema")
    stub = {"schema": "pw-training-candidate/1", "session": record["session_id"], "message_id": record["message_id"],
            "call_id": record.get("call_id", "")}
    if record.get("exclusion_reason"):
        return dict(stub, host_exclusion=record["exclusion_reason"])
    if record.get("dispatches") != 1 or not record.get("settled"):
        return dict(stub, host_exclusion="unsettled_or_ambiguous_dispatch")
    if record["epoch"] != identity["epoch"]:
        raise ValueError("foreign collection epoch")
    if bundle.contract_sha256 != identity["contract_sha256"]:
        raise ValueError("bundle from another host contract")
    student = (record.get("student_provider"), record.get("student_kind"), record.get("student_model"))
    if student != (identity["student_provider"], identity["student_kind"], identity["student_model"]):
        raise ValueError("capture rendered for another student")
    decode = SERVING_PROTOCOLS.get(record["student_kind"])
    if decode is None:
        raise ValueError("serving kind without checkpoint-template parity: " + str(record["student_kind"]))
    student_request, request, raw = (bundle.body(record, field) for field in ("student_request", "request", "response"))
    if student_request is None or request is None or raw is None:
        raise ValueError("missing captured request or response")
    messages, tools, controls = decode(student_request)
    output = bundle.completions.get(record["output_id"])
    if (output is None or output["session_id"] != record["session_id"] or output["message_id"] != record["message_id"]
            or output["scripted"] or not output["settled_at"]):
        raise ValueError("missing settled model output")
    target = response(raw)
    offered = {tool["function"]["name"] for tool in tools}
    capture = {"schema": "pw-model-request-capture/3", "request_id": record["call_id"],
               "session_id": record["session_id"], "message_id": record["message_id"],
               "host_revision": identity["host_commit"], "epoch": identity["epoch"],
               "contract_sha256": bundle.contract_sha256, "catalog_sha256": catalog_sha256,
               "student_provider": student[0], "student_kind": student[1], "student_model": student[2],
               "dispatch_sha256": digest(request),
               "state_sha256": digest({"request": student_request, "turn_attempt_id": output["turn_attempt_id"], "output_id": output["id"]}),
               "messages": messages, "tools": tools, "controls": controls}
    capture["sha256"] = digest(capture)
    execution = {"receipt_id": output["id"], "settled_at": output["settled_at"],
                 "calls": [call_facts(bundle, record, call, offered) for call in target["tool_calls"]],
                 "committed": not target["tool_calls"] and bundle.commits.get((record["session_id"], record["message_id"])) == output["id"]}
    return dict(stub, prompt=capture["messages"], target=target, request_capture=capture, execution=execution,
                response_sha256=digest(target), prompt_surface=record.get("prompt_surface"))
