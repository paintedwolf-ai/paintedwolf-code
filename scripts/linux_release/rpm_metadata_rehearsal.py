"""Build and lint production RPM metadata with a tiny nonapplication payload."""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import platform
import os
import re
import subprocess
import tempfile

from release_semver import linux_package_version, parse

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "ubuntu@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55"


def run(arguments, **kwargs):
    result = subprocess.run(arguments, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, **kwargs)
    print(result.stdout, end="", flush=True)
    return result


def identity(kind):
    value = os.environ.get("PW_RPM_FIXTURE_" + kind.upper())
    if value is not None:
        if not re.fullmatch("[a-f0-9]{40}", value):
            raise ValueError("invalid fixture source identity")
        return value
    reference = "HEAD" if kind == "commit" else "HEAD^{tree}"
    return subprocess.check_output(["git", "rev-parse", reference], cwd=ROOT, text=True).strip()


def rehearse(work):
    if platform.system() != "Linux" or platform.machine() not in {"x86_64", "aarch64"}:
        raise ValueError("RPM metadata qualification requires native Linux")
    lint_version = subprocess.check_output(["rpmlint", "--version"], text=True).strip()
    if lint_version != "2.5.0":
        raise ValueError("this regression requires actual rpmlint 2.5.0")
    spec = importlib.util.spec_from_file_location("native_package_assemble", ROOT / "scripts/linux-package-assemble.py")
    assembler = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(assembler)
    tree = work / "payload"
    resource = tree / "opt/painted-wolf-code/metadata-fixture.txt"
    resource.parent.mkdir(parents=True)
    resource.write_text("Explicit nonapplication metadata qualification fixture.\n")
    version = linux_package_version(parse((ROOT / "VERSION").read_text().strip()))
    architecture = platform.machine()
    outcomes = {}
    for name in ("current", "spelling-regression"):
        directory = work / name
        directory.mkdir()
        top = directory / "rpmbuild"
        top.mkdir()
        content = assembler.rpm_spec(ROOT, tree, version, 3)
        if name == "spelling-regression":
            # The historical failure differs only in the two real metadata fields.
            meta = assembler.tauri_metadata(ROOT)
            content = content.replace(meta["shortDescription"], "Agentic coding assistant with a local security engine")
            content = content.replace("Git tools", "Git toolchain")
        specification = directory / "painted-wolf-code.spec"
        specification.write_text(content)
        built = run(["rpmbuild", "-bb", "--quiet", "--target", architecture,
                     "--define", f"_topdir {top}", "--define", f"_rpmdir {directory}",
                     "--define", "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm",
                     str(specification)])
        if built.returncode:
            raise ValueError("actual tiny RPM construction failed")
        packages = list(directory.glob("*.rpm"))
        if len(packages) != 1:
            raise ValueError("construction did not produce exactly one RPM")
        lint = run(["rpmlint", "--config", str(ROOT / "packaging/linux/rpm/rpmlint.toml"), str(packages[0])])
        if (lint.returncode == 0) != (name == "current"):
            raise ValueError("production metadata must pass and historical spelling metadata must fail")
        outcomes[name] = {"rpmlint_exit_code": lint.returncode,
                          "rpm_sha256": hashlib.sha256(packages[0].read_bytes()).hexdigest(),
                          "specification_sha256": hashlib.sha256(specification.read_bytes()).hexdigest()}
    return {"format_version": 1, "application_qualified": False, "published": False,
            "source_commit": identity("commit"),
            "source_tree": identity("tree"),
            "architecture": architecture, "rpmlint_version": lint_version, "cases": outcomes}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--container", action="store_true")
    args = parser.parse_args()
    if args.receipt.exists() or args.receipt.is_symlink():
        parser.error("receipt must be a fresh path")
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    if args.container:
        command = ["docker", "run", "--rm", "--security-opt=no-new-privileges:true",
                   "--mount", f"type=bind,src={ROOT},dst=/source,readonly",
                   "--mount", f"type=bind,src={args.receipt.parent.resolve()},dst=/receipt",
                   "--env", "PW_RPM_FIXTURE_COMMIT=" + identity("commit"),
                   "--env", "PW_RPM_FIXTURE_TREE=" + identity("tree"),
                   IMAGE, "bash", "-euc", "apt-get update && apt-get install -y --no-install-recommends rpm rpmlint python3 git aspell-en && git config --global --add safe.directory /source && PYTHONPATH=/source/scripts python3 -m linux_release.rpm_metadata_rehearsal --receipt /receipt/" + args.receipt.name]
        subprocess.run(command, check=True)
        return
    with tempfile.TemporaryDirectory(prefix="paintedwolf-rpm-metadata-") as temporary:
        report = rehearse(Path(temporary))
    with args.receipt.open("x") as output:
        output.write(json.dumps(report, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
