"""Install pinned Go analyzers and validate cached binaries against their build metadata."""

import argparse
import os
from pathlib import Path
import re
import subprocess
import tempfile

from artifact_paths import bin_dir

ROOT = Path(__file__).resolve().parent.parent
TOOLS = {
    "golangci-lint": ("scripts/lint-go.sh", "GOLANGCI_VERSION", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint", "github.com/golangci/golangci-lint/v2"),
    "deadcode": ("scripts/deadcode-check.sh", "DEADCODE_VERSION", "golang.org/x/tools/cmd/deadcode", "golang.org/x/tools"),
    "govulncheck": ("scripts/govulncheck-gate.py", "GOVULNCHECK_VERSION", "golang.org/x/vuln/cmd/govulncheck", "golang.org/x/vuln"),
}
SETS = {"lint": ["golangci-lint", "deadcode"], "vulnerabilities": ["govulncheck"], "all": list(TOOLS)}


def pin(name, root=ROOT):
    source, variable, package, module = TOOLS[name]
    versions = re.findall(rf'^{variable}\s*=\s*"(v\d+\.\d+\.\d+)"$', (root / source).read_text(), re.M)
    if len(versions) != 1:
        raise ValueError(f"{source} must declare one {variable} version")
    return package, module, versions[0]


def toolchain(root=ROOT):
    versions = re.findall(r"^go (\d+\.\d+(?:\.\d+)?)$", (root / "lycaon/go.mod").read_text(), re.M)
    if len(versions) != 1:
        raise ValueError("lycaon/go.mod must declare one Go toolchain")
    return "go" + versions[0]


def matches(binary, module, version, compiler):
    if not binary.is_file() or not os.access(binary, os.X_OK):
        return False
    result = subprocess.run(["go", "version", "-m", str(binary)], capture_output=True, text=True, check=False)
    lines = result.stdout.splitlines()
    if result.returncode or not lines or lines[0].rsplit(": ", 1)[-1] != compiler:
        return False
    return any(line.split()[:3] == ["mod", module, version] for line in lines[1:])


def ensure(name, root=ROOT, directory=None, compiler=None):
    directory = directory or bin_dir(root)
    compiler = compiler or toolchain(root)
    package, module, version = pin(name, root)
    binary = directory / name
    if matches(binary, module, version, compiler):
        return binary
    directory.mkdir(parents=True, exist_ok=True)
    print(f"Installing {name} {version} with {compiler}", flush=True)
    with tempfile.TemporaryDirectory(prefix=f".{name}-", dir=directory) as temporary:
        env = {**os.environ, "GOTOOLCHAIN": compiler, "GOBIN": temporary}
        subprocess.run(["go", "install", f"{package}@{version}"], cwd=root / "lycaon", env=env, check=True)
        built = Path(temporary) / name
        if not matches(built, module, version, compiler):
            raise ValueError(f"{name} did not produce the declared module version and toolchain")
        os.replace(built, binary)
    return binary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["ensure", "key"])
    parser.add_argument("tools", choices=[*TOOLS, *SETS])
    args = parser.parse_args()
    names = SETS.get(args.tools, [args.tools])
    if args.command == "key":
        print("-".join([toolchain(), *(f"{name}-{pin(name)[2]}" for name in names)]))
    else:
        for name in names:
            ensure(name)


if __name__ == "__main__":
    main()
