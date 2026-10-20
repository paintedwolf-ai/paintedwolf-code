"""Exact request rendering and completion-only labels, without truncation."""
import json

from common import digest, read_rows
from jsonschema import Draft202012Validator


def check_row(row):
    capture, validation = row["request_capture"], row["validation"]
    if capture.get("schema") not in ("pw-model-request-capture/1", "pw-model-request-capture/2") or validation.get("schema") != "pw-training-validation/1":
        raise ValueError("unsupported evidence schema")
    if not capture.get("messages") or any(m.get("content") is not None and not isinstance(m["content"], str) for m in capture["messages"]):
        raise ValueError("unsupported request messages")
    if not any(m.get("role") in ("system", "developer") and m.get("content") for m in capture["messages"]):
        raise ValueError("missing host guidance")
    if capture["sha256"] != digest({k:v for k,v in capture.items() if k != "sha256"}):
        raise ValueError("capture digest mismatch")
    if row["prompt"] != capture["messages"] or validation["request_sha256"] != capture["sha256"]:
        raise ValueError("request mismatch")
    for key in ("host_revision", "epoch", "tool_surface", "state_sha256", "catalog_sha256"):
        if not capture.get(key) or validation.get(key) != capture[key]:
            raise ValueError("validation identity mismatch")
    if validation.get("validator") != "paintedwolf-host-training-validation/1" or not validation.get("receipt_id"):
        raise ValueError("host acceptance missing")
    target = row.get("target", row.get("chosen"))
    quality = validation.get("quality", {})
    if quality.get("verdict") != "pass" or not quality.get("evidence_ids") or quality.get("request_sha256") != capture["sha256"] or quality.get("response_sha256") != digest(target):
        raise ValueError("independent quality binding missing")
    if quality.get("method") == "cross_family_judge":
        if not quality.get("judge_family") or not quality.get("teacher_family") or quality["judge_family"] == quality["teacher_family"]:
            raise ValueError("judge must be cross-family")
    elif quality.get("method") != "deterministic":
        raise ValueError("unsupported quality method")
    if row["schema"] == "pw-custom-sft/1":
        if validation.get("outcome") != "accepted" or validation.get("response_origin") != "model" or validation.get("response_sha256") != digest(target):
            raise ValueError("response acceptance mismatch")
    elif row["schema"] == "pw-custom-dpo/1":
        if (validation.get("chosen_outcome") != "accepted" or validation.get("rejected_outcome") != "rejected"
                or validation.get("preference") != "chosen" or row["chosen"] == row["rejected"]
                or any(validation.get(k + "_sha256") != digest(row[k]) for k in ("chosen", "rejected"))):
            raise ValueError("preference acceptance mismatch")
    else:
        raise ValueError("unknown training schema")
    schemas = {}
    for tool in capture["tools"]:
        function = tool["function"]
        schema = function["parameters"]
        if tool["type"] != "function" or function["name"] in schemas or schema == {"type":"object"}:
            raise ValueError("invalid captured tool schema")
        Draft202012Validator.check_schema(schema)
        schemas[function["name"]] = schema
    if target.get("role") != "assistant" or not isinstance(target.get("text"), str) or not isinstance(target.get("tool_calls"), list):
        raise ValueError("malformed target")
    if not target["text"] and not target["tool_calls"]:
        raise ValueError("empty target")
    for call in target["tool_calls"]:
        if call["name"] not in schemas:
            raise ValueError("target calls an unoffered tool")
        Draft202012Validator(schemas[call["name"]]).validate(json.loads(call["arguments"]) if isinstance(call["arguments"], str) else call["arguments"])
    return row


def assistant(target):
    return {"role": "assistant", "content": target["text"], "tool_calls": [
        {"id": c["id"], "type": "function", "function": {"name": c["name"], "arguments": json.loads(c["arguments"]) if isinstance(c["arguments"], str) else c["arguments"]}}
        for c in target["tool_calls"]]}


def encode(tokenizer, row, target, max_length):
    capture = row["request_capture"]
    controls = capture.get("controls", {})
    if controls.get("tool_choice") not in (None, "auto") or controls.get("parallel_tool_calls") is False or controls.get("reasoning_effort") not in (None, "none", "minimal"):
        raise ValueError("request control needs a model-specific renderer")
    kwargs = {"tools": capture["tools"], "tokenize": False, "enable_thinking": False}
    prompt = tokenizer.apply_chat_template(row["prompt"], add_generation_prompt=True, **kwargs)
    full = tokenizer.apply_chat_template(row["prompt"] + [assistant(target)], add_generation_prompt=False, **kwargs)
    if not full.startswith(prompt):
        raise ValueError("chat template does not preserve the exact generation prefix")
    if target["text"] and target["text"] not in full[len(prompt):]:
        raise ValueError("template modified assistant content")
    prefix = tokenizer(prompt, add_special_tokens=False)["input_ids"]
    ids = tokenizer(full, add_special_tokens=False)["input_ids"]
    if ids[:len(prefix)] != prefix or len(ids) <= len(prefix):
        raise ValueError("token boundary crosses prompt/response; no guessed loss mask")
    if len(ids) > max_length:
        raise ValueError("oversize row; truncation is forbidden")
    return {"input_ids": ids, "labels": [-100] * len(prefix) + ids[len(prefix):]}


def load_split(path):
    rows = [check_row(row) for row in read_rows(path)]
    if not rows:
        raise ValueError("empty training split")
    return rows


def assert_disjoint(train, validation):
    # Export must assign a stable task-family group across workers and epochs.
    groups = []
    requests = []
    for rows in (train, validation):
        if any(not r.get("split_group") for r in rows):
            raise ValueError("missing stable split_group lineage")
        groups.append({r["split_group"] for r in rows})
        requests.append({digest(r["prompt"]) for r in rows})
    if groups[0] & groups[1] or requests[0] & requests[1]:
        raise ValueError("train/validation leakage")
