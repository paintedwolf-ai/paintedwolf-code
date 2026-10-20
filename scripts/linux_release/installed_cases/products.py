"""Fixed installed product probes; neither observations nor arbitrary commands are cases."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import stat

from .process import run

SCANNER_FILES = {"main.rs", "app.vue", "run.ps1", "safe.html", "ignored.vue", "data.proto", ".semgrepignore"}
PROBES = {
    ("documents", "pinned-scanner-execution"): ("scan", "engines", "verify-installed"),
    ("documents", "helper-startup"): ("diagnostics", "document-core"),
}


def require_owned_guest(binding: dict, marker=Path("/etc/paintedwolf-qualification.json")) -> dict:
    import os
    declared = marker.lstat()
    if declared.st_uid != 0:
        raise ValueError("owned guest declaration is not administrator selected")
    physical = marker.resolve(strict=True)
    info = physical.stat()
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022
            or info.st_size > 4096 or os.getuid() == 0):
        raise ValueError("installed execution requires an owned disposable guest and nonroot test account")
    data = json.loads(physical.read_text())
    if (not isinstance(data, dict) or set(data) != {"format_version", "desktop", "package"}
            or type(data["format_version"]) is not int or data["format_version"] != 1
            or data["desktop"] not in ("gnome", "plasma", "xfce", "cinnamon", "mate", "lxqt", "cosmic", "sway", "hyprland", "i3")
            or data["package"] not in (binding["install_root"], binding["artifact"]["identity"].get("store_path"))):
        raise ValueError("owned guest marker differs from the selected installed package")
    return data


def scanner_report(raw: bytes, binding: dict) -> dict:
    report = json.loads(raw)
    root = str(Path(binding["install_root"]) / "engine-root")
    if (not isinstance(report, dict) or type(report.get("schema_version")) is not int
            or report["schema_version"] != 1 or report.get("status") != "passed" or report.get("code")
            or report.get("confined") is not True or report.get("goos") != "linux"
            or report.get("goarch") != binding["artifact"]["architecture"] or report.get("engine_root") != root):
        raise ValueError("installed scanner did not qualify the selected native confined runtime")
    binary = report.get("binary")
    image = binding["images"].get(binary)
    if image is None or (report.get("binary_sha256"), report.get("binary_bytes")) != (image["sha256"], image["bytes"]):
        raise ValueError("scanner report does not identify a final installed executable")
    cases = report.get("cases")
    if (not isinstance(cases, list) or len(cases) != len(SCANNER_FILES)
            or {case.get("file") for case in cases if isinstance(case, dict)} != SCANNER_FILES
            or any(not isinstance(case, dict) or case.get("passed") is not True
                   or type(case.get("findings")) is not int or case["findings"] < 0 for case in cases)):
        raise ValueError("installed scanner omitted or failed a required product fixture")
    return report


def execute(binding: dict, capability: str, case: str, evidence_root: Path) -> dict:
    key = (capability, case)
    if key not in PROBES:
        return {"id": case, "status": "unverified", "evidence": [], "reason": "native-case-driver-required"}
    require_owned_guest(binding)
    arguments = list(PROBES[key])
    if key == ("documents", "pinned-scanner-execution"):
        arguments += ["--root", str(Path(binding["install_root"]) / "engine-root")]
    result = run(binding, "pw", arguments)
    passed = result["exit_code"] == 0 and not result["timed_out"] and not result["output_limit"]
    validation = None
    if passed and key == ("documents", "pinned-scanner-execution"):
        try:
            scanner_report(result["stdout"], binding)
        except (ValueError, TypeError) as error:
            passed, validation = False, str(error)
    directory = evidence_root / capability / case
    directory.mkdir(parents=True, exist_ok=False)
    records = []
    for stream in ("stdout", "stderr"):
        path = directory / (stream + ".bin")
        path.write_bytes(result.pop(stream))
        records.append({"path": path.relative_to(evidence_root).as_posix(), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
    result.update({"format_version": 1, "source_commit": binding["source_commit"],
                   "artifact": binding["artifact"], "capability": capability, "case": case,
                   "validation_error": validation})
    path = directory / "execution.json"
    path.write_text(json.dumps(result, indent=2) + "\n")
    records.append({"path": path.relative_to(evidence_root).as_posix(), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
    return {"id": case, "status": "passed" if passed else "failed", "evidence": records}
