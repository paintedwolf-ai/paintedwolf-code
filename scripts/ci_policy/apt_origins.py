"""Refresh and install Ubuntu packages on CI runners, moving off an unreachable archive origin."""
import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

APT_ROOT = Path("/etc/apt")
ATTEMPT_SECONDS = 100
ATTEMPTS = 3
OPTIONS = ["-o", "Acquire::Retries=1", "-o", "Acquire::http::Timeout=10", "-o", "Acquire::https::Timeout=10"]
CANONICAL = "https://archive.ubuntu.com/ubuntu"
# Runner origins and the Canonical HTTPS origin that serves the same suites under the same keys.
ORIGINS = [
    ("mirror+file:/etc/apt/apt-mirrors.txt", CANONICAL),
    ("http://azure.archive.ubuntu.com/ubuntu", CANONICAL),
    ("http://archive.ubuntu.com/ubuntu", CANONICAL),
    ("http://security.ubuntu.com/ubuntu", "https://security.ubuntu.com/ubuntu"),
    ("http://ports.ubuntu.com/ubuntu-ports", "https://ports.ubuntu.com/ubuntu-ports"),
]
UBUNTU_HOSTS = {"azure.archive.ubuntu.com", "archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com"}
FAILED_URI = re.compile(r"^(?:Err:\d+|[WE]: Failed to fetch)\s+(\S+)", re.MULTILINE)
PACKAGE = re.compile(r"[a-z0-9][a-z0-9+.-]*")


def apt_get(*arguments):
    command = ["sudo", "timeout", "--kill-after=10s", f"{ATTEMPT_SECONDS}s", "env", "DEBIAN_FRONTEND=noninteractive",
               "apt-get", *OPTIONS, *arguments]
    result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    sys.stdout.write(result.stdout)
    sys.stdout.flush()
    return result


def ubuntu_failures(output):
    """Index URIs on Ubuntu origins that apt could not fetch; other repositories stay warnings."""
    failed = []
    for uri in FAILED_URI.findall(output):
        host = uri.split("://", 1)[-1].split("/", 1)[0]
        if uri.startswith("mirror+file:") or host in UBUNTU_HOSTS:
            failed.append(uri)
    return failed


def source_files():
    directory = APT_ROOT / "sources.list.d"
    paths = [APT_ROOT / "sources.list", *sorted(directory.glob("*.list")), *sorted(directory.glob("*.sources"))]
    return [path for path in paths if path.is_file() and not path.is_symlink()]


def write(path, text):
    if os.access(path, os.W_OK):
        path.write_text(text)
    else:
        subprocess.run(["sudo", "tee", str(path)], input=text, text=True, stdout=subprocess.DEVNULL, check=True)


def use_canonical():
    """Point Ubuntu sources at Canonical's HTTPS archive; returns the rewritten files."""
    changed = []
    for path in source_files():
        text = path.read_text()
        replacement = text
        for runner, canonical in ORIGINS:
            replacement = replacement.replace(runner, canonical)
        if replacement != text:
            write(path, replacement)
            changed.append(path)
    if changed:
        print("Ubuntu package origin moved to Canonical's archive: " + ", ".join(map(str, changed)), flush=True)
    return changed


def update():
    """Refresh indexes, moving to Canonical's archive after an Ubuntu origin fails."""
    failures = []
    for attempt in range(1, ATTEMPTS + 1):
        result = apt_get("update")
        failed = ubuntu_failures(result.stdout)
        if result.returncode == 0 and not failed:
            return
        failures.append(f"attempt {attempt}: exit {result.returncode}" + (", failed " + " ".join(failed) if failed else ""))
        if attempt < ATTEMPTS:
            use_canonical()
    raise RuntimeError("Ubuntu package indexes are unavailable: " + "; ".join(failures))


def install(packages, recommends=True):
    if any(not PACKAGE.fullmatch(name) for name in packages):
        raise ValueError("packages must be Debian package names")
    update()
    if not packages:
        return
    arguments = ["--yes", *([] if recommends else ["--no-install-recommends"]), "install", "--", *packages]
    if apt_get(*arguments).returncode == 0:
        return
    use_canonical()
    update()
    result = apt_get(*arguments)
    if result.returncode != 0:
        raise RuntimeError(f"apt-get install failed with exit {result.returncode}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("update")
    commands.add_parser("canonical")
    installer = commands.add_parser("install")
    installer.add_argument("--no-recommends", action="store_true")
    installer.add_argument("packages", nargs="*")
    args = parser.parse_args(argv)
    if args.command == "update":
        update()
    elif args.command == "canonical":
        use_canonical()
    else:
        install(args.packages, recommends=not args.no_recommends)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, ValueError, subprocess.CalledProcessError) as error:
        print(f"error: {error}", file=sys.stderr)
        sys.exit(1)
