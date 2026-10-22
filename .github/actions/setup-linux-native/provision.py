"""Install the native package closure resolved from the runner's signed APT indexes."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
from urllib.parse import urlsplit, urlunsplit

ROOT = Path(__file__).resolve().parent
DOWNLOAD_SECONDS = 240
TRANSFER_SECONDS = 30


def packages():
    names = (ROOT / "packages.txt").read_text().splitlines()
    if not names or len(names) != len(set(names)) or any(not re.fullmatch(r"[a-z0-9][a-z0-9+.-]*", n) for n in names):
        raise ValueError("native packages must be unique Debian package names")
    return names


def apt(*arguments, capture=False):
    command = ["sudo", "timeout", "--kill-after=10s", "180s", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get",
               "-o", "Acquire::Retries=2", "-o", "Acquire::http::Timeout=20", "-o", "Acquire::https::Timeout=20",
               *arguments]
    result = subprocess.run(command, check=True, text=True, stdout=subprocess.PIPE if capture else None)
    return result.stdout


def runner_image(environment):
    fields = ("ImageOS", "ImageVersion", "RUNNER_ARCH")
    missing = [name for name in fields if not environment.get(name)]
    if missing:
        raise ValueError("native package caching requires GitHub-hosted runner metadata: " + ", ".join(missing))
    return [environment[name] for name in fields]


def archives(plan):
    rows = []
    for line in plan.splitlines():
        if not line.startswith("'"):
            continue
        fields = shlex.split(line)
        if (len(fields) != 4 or not fields[2].isdigit() or int(fields[2]) <= 0
                or not re.fullmatch(r"SHA256:[0-9a-fA-F]{64}", fields[3])
                or not re.fullmatch(r"[A-Za-z0-9.+~%:_-]+\.deb", fields[1])):
            raise ValueError("APT archive plan must contain safe filenames, sizes and SHA256 digests")
        uri = urlsplit(fields[0])
        if uri.scheme not in {"http", "https"} or not uri.hostname or uri.username or uri.password:
            raise ValueError("APT archive plan requires an HTTP package origin without credentials")
        rows.append(dict(uri=fields[0], name=fields[1], size=int(fields[2]), sha256=fields[3][7:].lower()))
    if len({row["name"] for row in rows}) != len(rows):
        raise ValueError("APT archive plan contains duplicate filenames")
    return rows


def archive_identity(plan, names, environment):
    rows = archives(plan)
    identity = {"packages": names,
                "archives": sorted([row["name"], str(row["size"]), "SHA256:" + row["sha256"]] for row in rows),
                "image": runner_image(environment)}
    return "linux-native-v1-" + hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()


def origins(uri):
    parsed = urlsplit(uri)
    result = [uri]
    if parsed.hostname in {"azure.archive.ubuntu.com", "archive.ubuntu.com", "security.ubuntu.com"} and parsed.path.startswith("/ubuntu/pool/"):
        fallback = urlunsplit(("https", "archive.ubuntu.com", parsed.path, "", ""))
        if fallback != uri:
            result.append(fallback)
    elif parsed.hostname == "ports.ubuntu.com" and parsed.scheme == "http" and parsed.path.startswith("/ubuntu-ports/pool/"):
        result.append(urlunsplit(("https", parsed.netloc, parsed.path, "", "")))
    return result


def verified(path, row):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != row["size"]:
        return False
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest() == row["sha256"]


def transfer(uri, path, row, deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("native package download deadline expired")
    command = ["curl", "--fail", "--location", "--silent", "--show-error",
               "--proto", "=http,https", "--proto-redir", "=http,https", "--connect-timeout", "15",
               "--max-time", str(remaining), "--max-filesize", str(row["size"]), "--output", str(path), "--", uri]
    try:
        subprocess.run(command, check=True, capture_output=True, text=True, timeout=remaining)
    except subprocess.TimeoutExpired as error:
        raise TimeoutError("native package transfer exceeded its deadline") from error
    except subprocess.CalledProcessError as error:
        raise OSError(f"native package transfer failed with curl exit {error.returncode}") from error
    if time.monotonic() >= deadline:
        raise TimeoutError("native package transfer exceeded its deadline")
    if not verified(path, row):
        raise ValueError("native package differs from its resolved SHA256 or size")


def fetch(row, directory, deadline):
    destination = directory / row["name"]
    if verified(destination, row):
        return "cached"
    temporary = directory / "partial" / row["name"]
    failures = []
    for uri in origins(row["uri"]) * 2:
        try:
            transfer(uri, temporary, row, min(deadline, time.monotonic() + TRANSFER_SECONDS))
            temporary.replace(destination)
            print(f"Native package verified: {row['name']} from {urlsplit(uri).hostname}", flush=True)
            return "downloaded"
        except (OSError, ValueError) as error:
            failures.append(f"{urlsplit(uri).hostname}: {error}")
        finally:
            temporary.unlink(missing_ok=True)
    raise ValueError(f"Unable to prepare {row['name']}: {'; '.join(failures)}")


def prepare(rows, directory):
    started = time.monotonic()
    deadline = started + DOWNLOAD_SECONDS
    with ThreadPoolExecutor(max_workers=4) as executor:
        results = list(executor.map(lambda row: fetch(row, directory, deadline), rows))
    summary = {"archives": len(rows), "cached": results.count("cached"), "downloaded": results.count("downloaded"),
               "download_seconds": round(time.monotonic() - started, 3)}
    print(json.dumps(summary, sort_keys=True), flush=True)
    return summary


def main(mode):
    if os.environ.get("RUNNER_OS") != "Linux":
        raise ValueError("native package preparation requires a Linux runner")
    image = runner_image(os.environ)
    names = packages()
    directory = Path(os.environ["RUNNER_TEMP"]) / "paintedwolf-native-archives"
    (directory / "partial").mkdir(parents=True, exist_ok=True)
    manifest = directory / "plan.json"
    if mode == "plan":
        apt("update")
        plan = apt("-o", "Acquire::ForceHash=SHA256", "--print-uris", "--yes", "--download-only", "--no-install-recommends", "install", *names, capture=True)
        key = archive_identity(plan, names, os.environ)
        manifest.write_text(json.dumps({"plan": plan, "packages": names, "image": image, "key": key}, indent=2) + "\n")
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"archives={directory}\nkey={key}\n")
    elif mode == "install":
        value = json.loads(manifest.read_text())
        if (value["packages"] != names or value["image"] != image
                or value["key"] != archive_identity(value["plan"], names, os.environ)):
            raise ValueError("native package plan no longer matches the runner and package inputs")
        summary = prepare(archives(value["plan"]), directory)
        apt("-o", f"Dir::Cache::archives={directory}", "-o", "APT::Keep-Downloaded-Packages=true",
            "--no-download", "--yes", "--no-install-recommends", "install", *names)
        if os.environ.get("GITHUB_STEP_SUMMARY"):
            with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
                output.write("### Native package preparation\n\n```json\n" + json.dumps(summary, indent=2) + "\n```\n")
    else:
        raise ValueError("expected plan or install")


if __name__ == "__main__":
    main(sys.argv[1])
