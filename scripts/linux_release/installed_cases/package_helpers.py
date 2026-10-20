"""Exercise sealed installed helper images and runtime modules through fixed probes."""
from __future__ import annotations

from pathlib import Path

from .app import InstalledApp
from .desktop_ui import interactive_window
from .process import run
from .products import require_owned_guest
from .records import CaseUnavailable, retain

PROBES = (
    ("document-runtime", "pw", ("diagnostics", "document-core"), 0),
    ("ocr-runtime", "pw", ("diagnostics", "ocr-engine"), 0),
    ("log-viewer", "pw-logs", ("--help",), 0),
    # This fixed Go flag parser reports flag.ErrHelp through its normal exit(1).
    ("vault-helper", "pw-vault-identity", ("--help",), 1),
    ("decision-helper", "bialy", ("--help",), 0),
    ("git-runtime", "git-observer", ("--version",), 0),
    ("git-lfs-runtime", "git-lfs-observer", ("version",), 0),
    ("browser-runtime", "browser-observer", ("--version",), 0),
    ("ocr-language-data", "ocr-observer", ("--list-langs",), 0),
)
CASES = {"all-helpers-and-resources", "elf-abi-and-runtime-modules"}


def execute(binding, capability, case, evidence_root: Path):
    if capability != "installation" or case not in CASES:
        raise ValueError("installed helper driver received a different fixed case")
    require_owned_guest(binding)
    observations = {"payload": binding["payload"], "probes": [], "output_limit": False,
                    "scope": "sealed native image startup, document/OCR runtime and installed desktop loading"}
    streams = {"helpers-stdout": b"", "helpers-stderr": b""}
    app, status = None, "failed"
    try:
        passed = True
        for name, role, arguments, expected_exit in PROBES:
            result = run(binding, role, list(arguments))
            observed = {"probe": name, "role": role, "arguments": list(arguments), "expected_exit": expected_exit}
            for channel in ("stdout", "stderr"):
                raw = result.pop(channel)
                stream = "helpers-" + channel
                offset = len(streams[stream])
                remaining = max(0, min((8 << 20) - offset, (24 << 20) - sum(map(len, streams.values()))))
                streams[stream] += raw[:remaining]
                observed[channel] = {"stream": stream, "offset": offset, "bytes": min(len(raw), remaining),
                                     "truncated": len(raw) > remaining}
                observations["output_limit"] |= len(raw) > remaining
            observed.update(result)
            observed["passed"] = (result["exit_code"] == expected_exit and not result["timed_out"]
                                  and not result["output_limit"])
            passed &= observed["passed"]
            observations["probes"].append(observed)
        if not passed or observations["output_limit"]:
            raise ValueError("a required sealed native helper did not complete its fixed startup probe")
        app = InstalledApp(binding)
        with app:
            observations["interactive_window"] = interactive_window(app)
            health = app.request("GET", "/health")
            observations["engine_health"] = {"status": health["status"]}
            if health["status"] != 200:
                raise ValueError("installed desktop did not load its selected native engine")
        if (passed and len(observations["probes"]) == len(PROBES) and not observations["output_limit"]
                and app.observations.get("capture_complete") and not app.observations.get("output_limit")):
            status = "passed"
    except (OSError, ValueError) as error:
        observations["validation_error"] = str(error)
        status = "unverified" if isinstance(error, CaseUnavailable) and all(item["passed"] for item in observations["probes"]) else "failed"
    finally:
        if app is not None:
            app.stop()
            observations["desktop"] = app.observations
            for name, raw in app.streams().items():
                remaining = max(0, min(8 << 20, (30 << 20) - sum(map(len, streams.values()))))
                if len(raw) > remaining:
                    observations["output_limit"], status = True, "failed"
                streams[name] = raw[:remaining]
    return retain(binding, capability, case, evidence_root, status, observations, streams)
