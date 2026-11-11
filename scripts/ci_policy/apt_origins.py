"""Refresh and install Ubuntu packages on CI runners, moving off an unreachable archive origin."""
import argparse
from concurrent.futures import ThreadPoolExecutor, TimeoutError as ResolveTimeout
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import time
from urllib.parse import urlsplit

APT_ROOT = Path(os.environ.get("PW_APT_ROOT", "/etc/apt"))
ATTEMPT_SECONDS = 100
RESOLVE_SECONDS = 10
OPTIONS = ["-o", "Acquire::Retries=1", "-o", "Acquire::http::Timeout=10", "-o", "Acquire::https::Timeout=10"]
# Every origin serves the same suites, and apt checks them against the Ubuntu archive keys.
FAMILIES = {
    "archive": r"mirror\+file:/etc/apt/apt-mirrors\.txt|https?://(?:azure\.)?archive\.ubuntu\.com/ubuntu"
               r"|https://mirrors\.edge\.kernel\.org/ubuntu|https://mirrors\.ocf\.berkeley\.edu/ubuntu",
    "security": r"https?://security\.ubuntu\.com/ubuntu",
    "ports": r"https?://ports\.ubuntu\.com/ubuntu-ports|https://mirrors\.ocf\.berkeley\.edu/ubuntu-ports"
             r"|https://mirror\.us\.leaseweb\.net/ubuntu-ports",
}
ORIGIN = re.compile("(?:" + "|".join(f"(?P<{name}>{pattern})" for name, pattern in FAMILIES.items()) + r")(?=/|\s|$)")
# Fallbacks after the runner's configured origin: Canonical, then mirrors outside ubuntu.com
# for when the runner cannot resolve or reach ubuntu.com hosts.
STAGES = [
    ("Canonical's archive", {"archive": "https://archive.ubuntu.com/ubuntu",
                             "security": "https://security.ubuntu.com/ubuntu",
                             "ports": "https://ports.ubuntu.com/ubuntu-ports"}),
    ("an Ubuntu mirror outside ubuntu.com", {"archive": "https://mirrors.edge.kernel.org/ubuntu",
                                             "security": "https://mirrors.edge.kernel.org/ubuntu",
                                             "ports": "https://mirrors.ocf.berkeley.edu/ubuntu-ports"}),
    ("a second Ubuntu mirror outside ubuntu.com", {"archive": "https://mirrors.ocf.berkeley.edu/ubuntu",
                                                   "security": "https://mirrors.ocf.berkeley.edu/ubuntu",
                                                   "ports": "https://mirror.us.leaseweb.net/ubuntu-ports"}),
]
UBUNTU_HOSTS = {"azure.archive.ubuntu.com"} | {urlsplit(uri).hostname for _, origins in STAGES for uri in origins.values()}
FAILED_URI = re.compile(r"^(?:Err:\d+|[WE]: Failed to fetch)\s+(\S+)", re.MULTILINE)
PACKAGE = re.compile(r"[a-z0-9][a-z0-9+.-]*")
next_stage = 0


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


def origin_hosts():
    """Host groups apt needs for the Ubuntu sources; a group resolves when any of its hosts does."""
    groups = set()
    for path in source_files():
        for match in ORIGIN.finditer(path.read_text()):
            if match.group(0).startswith("mirror+file:"):
                mirrors = APT_ROOT / "apt-mirrors.txt"
                lines = mirrors.read_text().splitlines() if mirrors.is_file() else []
                hosts = {urlsplit(line.split()[0]).hostname for line in lines if line.strip() and not line.startswith("#")}
                groups.add(frozenset(host for host in hosts if host))
            else:
                groups.add(frozenset([urlsplit(match.group(0)).hostname]))
    return groups


def unresolved():
    """Ubuntu origin host groups whose names cannot be resolved, checked concurrently with a bound."""
    groups = origin_hosts()
    hosts = sorted(set().union(*groups)) if groups else []
    pool = ThreadPoolExecutor(max_workers=max(len(hosts), 1))
    lookups = {host: pool.submit(socket.getaddrinfo, host, 443, type=socket.SOCK_STREAM) for host in hosts}
    deadline = time.monotonic() + RESOLVE_SECONDS
    resolved = set()
    for host, lookup in lookups.items():
        try:
            lookup.result(timeout=max(deadline - time.monotonic(), 0))
            resolved.add(host)
        except (OSError, ResolveTimeout):
            pass
    pool.shutdown(wait=False, cancel_futures=True)
    return sorted(" or ".join(sorted(group)) for group in groups if not group & resolved)


def write(path, text):
    if os.access(path, os.W_OK):
        path.write_text(text)
    else:
        subprocess.run(["sudo", "tee", str(path)], input=text, text=True, stdout=subprocess.DEVNULL, check=True)


def use_stage(index):
    """Point Ubuntu sources at one fallback stage; returns the rewritten files."""
    label, origins = STAGES[index]
    changed = []
    for path in source_files():
        text = path.read_text()
        replacement = ORIGIN.sub(lambda match: origins[match.lastgroup], text)
        if replacement != text:
            write(path, replacement)
            changed.append(path)
    if changed:
        print(f"Ubuntu package origin moved to {label}: " + ", ".join(map(str, changed)), flush=True)
    return changed


def fail_over():
    """Move to the next stage that changes the sources; False once none remain."""
    global next_stage
    while next_stage < len(STAGES):
        next_stage += 1
        if use_stage(next_stage - 1):
            return True
    return False


def update():
    """Refresh indexes, moving to the next fallback stage after an Ubuntu origin fails."""
    failures = []
    while True:
        dead = unresolved()
        if dead:
            failures.append(f"attempt {len(failures) + 1}: cannot resolve " + ", ".join(dead))
            print(f"Ubuntu package origin cannot be resolved: {', '.join(dead)}", flush=True)
        else:
            result = apt_get("update")
            failed = ubuntu_failures(result.stdout)
            if result.returncode == 0 and not failed:
                return
            failures.append(f"attempt {len(failures) + 1}: exit {result.returncode}"
                            + (", failed " + " ".join(failed) if failed else ""))
        if not fail_over():
            raise RuntimeError("Ubuntu package indexes are unavailable: " + "; ".join(failures))


def install(packages, recommends=True):
    if any(not PACKAGE.fullmatch(name) for name in packages):
        raise ValueError("packages must be Debian package names")
    update()
    if not packages:
        return
    arguments = ["--yes", *([] if recommends else ["--no-install-recommends"]), "install", "--", *packages]
    result = apt_get(*arguments)
    if result.returncode != 0 and fail_over():
        update()
        result = apt_get(*arguments)
    if result.returncode != 0:
        raise RuntimeError(f"apt-get install failed with exit {result.returncode}")


def fallback_uris(uri):
    """The same archive path on each fallback stage, after the URI itself."""
    match = ORIGIN.match(uri)
    if not match:
        return [uri]
    rest = uri[match.end():]
    result = [] if uri.startswith("mirror+file:") else [uri]
    for _, origins in STAGES:
        candidate = origins[match.lastgroup] + rest
        if candidate not in result:
            result.append(candidate)
    return result


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
        use_stage(0)
    else:
        install(args.packages, recommends=not args.no_recommends)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, ValueError, subprocess.CalledProcessError) as error:
        print(f"error: {error}", file=sys.stderr)
        sys.exit(1)
