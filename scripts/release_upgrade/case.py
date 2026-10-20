"""Exercise native staging, startup activation, and retained store semantics."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys

from release_upgrade.prepare import archive_path, file_digest
from release_upgrade.process import Installation, read_json, wait_for

SCRIPTS = Path(__file__).resolve().parent.parent


def materialize(fixture: Path, config: Path, project: Path):
    subprocess.run([sys.executable, str(SCRIPTS / "upgrade-fixture-files.py"), "materialize", str(fixture), str(config)], check=True, timeout=120)
    shutil.copytree(fixture / "project-root", project)
    (project / "overlay-dir").rename(project / ".paintedwolf")
    with sqlite3.connect(config / "store.db") as database:
        database.execute("UPDATE project_roots SET path = ? WHERE path = 'project-root' OR path LIKE 'project-root/%'", (str(project),))


def verify_semantics(config: Path, fixture: Path, observation: dict, evidence: Path):
    with (evidence / "semantics.log").open("wb") as log:
        subprocess.run([sys.executable, str(SCRIPTS / "upgrade-corpus-semantics.py"), "verify",
                        "--base", f"http://127.0.0.1:{observation['port']}", "--token-file", str(config / "api.token"),
                        "--fixture", str(fixture), "--config-dir", str(config)], stdout=log, stderr=log, check=True, timeout=120,
                       env={**os.environ, "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost"})


def verify_preferences(path: Path, channel: str):
    preferences = read_json(path)
    expected = {"format_version": 2, "channel": channel, "automatic_updates_enabled": True, "rollout_bucket": 0}
    if not isinstance(preferences, dict) or any(type(preferences.get(key)) is not type(value) or preferences.get(key) != value for key, value in expected.items()):
        raise ValueError("automatic update preferences changed during activation")


def staged(records: Path, diagnostics: Path | None = None, offset: int = 0):
    if read_json(records / "rejected.json") is not None:
        raise ValueError("native updater rejected the signed candidate")
    transaction = read_json(records / "transaction.json")
    if transaction and transaction.get("phase") == "failed":
        raise ValueError("native update transaction failed")
    if diagnostics is not None and diagnostics.is_file():
        data = diagnostics.read_bytes()
        data = data[offset:] if len(data) >= offset else data
        for line in data.splitlines(keepends=True):
            if not line.endswith(b"\n"):
                continue
            try:
                record = json.loads(line)
            except ValueError:
                continue
            error = record.get("error") if isinstance(record, dict) else None
            code = error.get("code") if isinstance(error, dict) else None
            if isinstance(code, str) and code:
                raise ValueError("Native updater failed: " + code)
    return read_json(records / "ready.json")


def candidate_matches(candidate: dict, step: dict, channel: str) -> bool:
    artifact = step["platforms"]["darwin-aarch64"]
    expected = {"version": step["version"], "channel": channel, "platform": "darwin-aarch64",
                "signing_generation": step["update_keys"]["signing_generation"],
                "artifact_url": artifact["url"], "artifact_signature": artifact["signature"]}
    return all(candidate.get(key) == value for key, value in expected.items())


def confirmed(transaction: dict | None, version: str, app: Path) -> bool:
    if not transaction:
        return False
    if transaction.get("phase") == "failed":
        raise ValueError("native update transaction failed")
    if transaction.get("phase") != "startup_confirmed":
        return False
    if transaction.get("candidate", {}).get("version") != version or transaction.get("target") != str(app):
        raise ValueError("startup confirmation belongs to another installation or release")
    previous = Path(transaction["prepared_bundle"])
    if previous.parent != app.parent or not previous.name.startswith(".paintedwolf-update-"):
        raise ValueError("startup confirmation has an unexpected previous bundle path")
    return not previous.exists()


def run_case(case: dict, plan: dict, root: Path, work: Path, config: Path, evidence: Path):
    if case["protocol"] != "native-v2":
        raise ValueError("desktop gate covers automatic updater clients only")
    evidence.mkdir()
    fixture = root / "fixtures" / case["source_version"]
    materialize(fixture, config, work / "project")
    preferences = config / "updates/preferences.json"
    preferences.parent.mkdir(parents=True, exist_ok=True)
    preferences.write_text(json.dumps({"format_version": 2, "automatic_updates_enabled": True,
                                      "channel": case["channel"], "rollout_bucket": 0}))
    archive = root / archive_path(plan["archives"][case["source_version"]])
    subprocess.run(["/usr/bin/tar", "-xzf", str(archive), "-C", str(work)], check=True, timeout=120)
    apps = list(work.glob("*.app"))
    if len(apps) != 1:
        raise ValueError("prior archive must contain exactly one app")
    app = apps[0].resolve()
    subprocess.run(["codesign", "--verify", "--deep", "--strict", str(app)], check=True, timeout=120)
    installation = Installation(app, config)
    namespace = hashlib.sha256(str(app).encode()).hexdigest()
    records = config / "updates/installations" / namespace
    observations = []
    diagnostics = config / "updates.log"
    diagnostic_offset = diagnostics.stat().st_size if diagnostics.is_file() else 0
    try:
        installation.launch()
        before = installation.healthy(case["source_version"])
        if before["health"].get("schema_version") != plan["schemas"][case["source_version"]]:
            raise ValueError("prior engine does not match its exact corpus schema")
        verify_semantics(config, fixture, before, evidence)
        for step in case["steps"]:
            version = step["version"]
            ready = wait_for(f"{version} was not automatically staged", lambda: staged(records, diagnostics, diagnostic_offset), 600)
            if not candidate_matches(ready.get("candidate", {}), step, case["channel"]):
                raise ValueError("automatic staging selected an unexpected release")
            (evidence / f"{version}-ready.json").write_text(json.dumps(ready, indent=2) + "\n")
            prepared = Path(ready["prepared_bundle"])
            prepared_inode = prepared.stat().st_ino
            prior_inode = app.stat().st_ino
            installation.stop(crash_app=True)
            if app.stat().st_ino != prior_inode:
                raise ValueError("the staged source activated before its startup rehearsal")
            installation.launch()
            after = installation.healthy(version)
            verify_preferences(preferences, case["channel"])
            wait_for(f"{version} did not confirm startup and remove the previous bundle",
                     lambda: confirmed(read_json(records / "transaction.json"), version, app), 180)
            if app.stat().st_ino != prepared_inode or app.stat().st_ino == prior_inode:
                raise ValueError("activation did not exchange the prepared bundle inode")
            transaction = read_json(records / "transaction.json")
            if transaction["candidate"]["release_id"] != ready["candidate"]["release_id"]:
                raise ValueError("activation confirmed a different staged release")
            if file_digest(installation.executable) != transaction["next_hash"]:
                raise ValueError("installed executable differs from its activation receipt")
            expected = plan["schema_version"] if version == plan["candidate"]["version"] else plan["schemas"][version]
            if after["health"].get("schema_version") != expected:
                raise ValueError("upgraded engine has an unexpected schema")
            (evidence / f"{version}-transaction.json").write_text(json.dumps(transaction, indent=2) + "\n")
            observations.append({"version": version, "health": after["health"], "transaction_id": transaction["id"]})
        verify_semantics(config, fixture, after, evidence)
        installation.stop()
        installation.launch()
        restart = installation.healthy(plan["candidate"]["version"])
        verify_preferences(preferences, case["channel"])
        verify_semantics(config, fixture, restart, evidence)
        return {"id": case["id"], "result": "passed", "activation_trigger": "startup_after_staging",
                "steps": observations, "restart_health": restart["health"]}
    finally:
        try:
            installation.stop()
        finally:
            retain_state(config, records, evidence)


def retain_state(config, records, evidence):
    for name in ("ready.json", "rejected.json", "transaction.json"):
        if (records / name).is_file():
            shutil.copyfile(records / name, evidence / ("final-" + name))
    for name in ("engine.log", "engine.log.1", "updates.log", "updates.log.1"):
        if (config / name).is_file():
            shutil.copyfile(config / name, evidence / name)
