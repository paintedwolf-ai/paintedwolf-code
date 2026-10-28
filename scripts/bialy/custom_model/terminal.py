"""Machine-readable training outcomes bound to one invocation and its inputs."""
import errno
import json
import sys
import uuid
from pathlib import Path

from checkpoint import atomic_json
from common import digest, file_digest, inventory


def failure(exc):
    torch = sys.modules.get("torch")
    oom = getattr(torch, "OutOfMemoryError", MemoryError)
    if isinstance(exc, (MemoryError, oom)):
        return {"code": "out_of_memory", "retryable": False}
    if isinstance(exc, (ConnectionError, TimeoutError)) or (
        isinstance(exc, OSError) and exc.errno in {errno.ECONNRESET, errno.ETIMEDOUT, errno.EHOSTUNREACH}
    ):
        return {"code": "transport_unavailable", "retryable": True}
    if isinstance(exc, (ValueError, KeyError, TypeError)):
        return {"code": "invalid_training_inputs", "retryable": False}
    if isinstance(exc, OSError):
        return {"code": "storage_failure", "retryable": False}
    return {"code": "training_failure", "retryable": False}


def invocation(args):
    config = {key: str(value) if isinstance(value, Path) else value
              for key, value in vars(args).items() if key not in {"resume"}}
    return {"schema": "pw-training-terminal/1", "attempt_id": uuid.uuid4().hex,
            "invocation_sha256": digest(config), "status": "running"}


def completed(root, record):
    root = Path(root)
    result = json.loads((root / "result.json").read_text())
    inputs = json.loads((root / "inputs.json").read_text())
    if result.get("inputs_sha256") != digest(inputs) or result.get("adapter") != inventory(root / "adapter"):
        raise ValueError("training result does not match its inputs and adapter")
    if result.get("resources", {}).get("status") != "complete":
        raise ValueError("training result is not complete")
    return dict(record, status="complete", inputs_sha256=digest(inputs),
                result_sha256=file_digest(root / "result.json"))


def execute(args, trainer):
    record = invocation(args)
    path = args.out / "terminal.json"
    atomic_json(path, record)
    try:
        result = trainer(args)
        if result.get("status") in {"paused", "paused_validation"}:
            record = dict(record, status="paused", progress=result)
        else:
            record = completed(args.out, record)
    except Exception as exc:
        atomic_json(path, dict(record, status="failed", failure=failure(exc),
                               exception_type=type(exc).__name__, diagnostic=str(exc)))
        raise
    atomic_json(path, record)
    return result
