"""Shared pieces of the package metadata rehearsals: a tiny payload, source identity and the command line."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "ubuntu@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55"
# Everything either rehearsal runs; a pull-request lane installs the same set before it goes offline.
PACKAGES = ("aspell-en", "git", "lintian", "python3", "rpm", "rpmlint")


def run(arguments, **kwargs):
    result = subprocess.run(arguments, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, **kwargs)
    print(result.stdout, end="", flush=True)
    return result


def identity(kind):
    value = os.environ.get("PW_METADATA_FIXTURE_" + kind.upper())
    if value is not None:
        if not re.fullmatch("[a-f0-9]{40}", value):
            raise ValueError("invalid fixture source identity")
        return value
    reference = "HEAD" if kind == "commit" else "HEAD^{tree}"
    return subprocess.check_output(["git", "rev-parse", reference], cwd=ROOT, text=True).strip()


def native_architecture():
    if platform.system() != "Linux" or platform.machine() not in {"x86_64", "aarch64"}:
        raise ValueError("package metadata qualification requires native Linux")
    return platform.machine()


def assembler():
    spec = importlib.util.spec_from_file_location("native_package_assemble", ROOT / "scripts/linux-package-assemble.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def payload(tree):
    """Entry points and one resource: enough for the package tools, with none of the application."""
    resource = tree / "opt/painted-wolf-code/metadata-fixture.txt"
    resource.parent.mkdir(parents=True)
    resource.write_text("Explicit nonapplication metadata qualification fixture.\n")
    binaries = tree / "usr/bin"
    binaries.mkdir(parents=True)
    for name in ("pw", "pw-logs"):
        shutil.copyfile("/usr/bin/true", binaries / name)
        (binaries / name).chmod(0o755)
    return tree


def report(architecture, **fields):
    return {"format_version": 1, "application_qualified": False, "published": False,
            "source_commit": identity("commit"), "source_tree": identity("tree"),
            "architecture": architecture, **fields}


def main(module, description, rehearse):
    parser = argparse.ArgumentParser(description=description)
    parser.add_argument("--receipt", type=Path, help="fresh path for the rehearsal report; printed when omitted")
    parser.add_argument("--container", action="store_true", help="run inside the pinned image with its own tools")
    args = parser.parse_args()
    receipt = args.receipt
    if receipt is not None:
        if receipt.exists() or receipt.is_symlink():
            parser.error("receipt must be a fresh path")
        receipt.parent.mkdir(parents=True, exist_ok=True)
    if args.container:
        mount, output = [], ""
        if receipt is not None:
            mount = ["--mount", f"type=bind,src={receipt.parent.resolve()},dst=/receipt"]
            output = " --receipt /receipt/" + receipt.name
        subprocess.run(["docker", "run", "--rm", "--security-opt=no-new-privileges:true",
                        "--mount", f"type=bind,src={ROOT},dst=/source,readonly", *mount,
                        "--env", "PW_METADATA_FIXTURE_COMMIT=" + identity("commit"),
                        "--env", "PW_METADATA_FIXTURE_TREE=" + identity("tree"),
                        IMAGE, "bash", "-euc",
                        "export DEBIAN_FRONTEND=noninteractive; apt-get update && apt-get install -y --no-install-recommends "
                        + " ".join(PACKAGES) + " && git config --global --add safe.directory /source && cd /source"
                        f" && PYTHONPATH=/source/scripts python3 -m {module}{output}"], check=True)
        return
    with tempfile.TemporaryDirectory(prefix="paintedwolf-package-metadata-") as temporary:
        document = json.dumps(rehearse(Path(temporary)), indent=2, sort_keys=True) + "\n"
    if receipt is None:
        sys.stdout.write(document)
        return
    with receipt.open("x") as output:
        output.write(document)


if __name__ == "__main__":
    print(" ".join(PACKAGES))
