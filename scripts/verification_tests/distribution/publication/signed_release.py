"""Committed signed release fixtures and the process and storage boundaries they cross.

The documents, catalog, updater manifest and packages are real inputs to the
distribution constructor; the signatures carry the maintained generation-1 key
identity. Only the minisign process and remote storage are replaced, by fakes that
answer from these committed bytes so every metadata, digest and trust check runs.
"""
import base64
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
from unittest.mock import patch

FIXTURE = Path(__file__).with_name("fixtures") / "signed-release"
MISMATCH = FIXTURE / "mismatch"
PREVIOUS = FIXTURE / "previous"
DOCUMENT = "distribution.v1.json"
SIGNATURE = DOCUMENT + ".sig"


def source_repository(root, files):
    """Commit an immutable application source whose release facts are read from Git objects."""
    root.mkdir(parents=True)
    contents = {"VERSION": "1.2.3\n", "RELEASE_BUILD": "14\n",
                "packaging/release-platforms.json": (FIXTURE / "catalog.json").read_text(), **files}
    for name, text in contents.items():
        (root / name).parent.mkdir(parents=True, exist_ok=True)
        (root / name).write_text(text)
    environment = {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_AUTHOR_NAME": "Fixture",
                   "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_NAME": "Fixture",
                   "GIT_COMMITTER_EMAIL": "fixture@example.invalid", "GIT_AUTHOR_DATE": "2026-10-20T00:00:00Z",
                   "GIT_COMMITTER_DATE": "2026-10-20T00:00:00Z", "PATH": os.environ.get("PATH", os.defpath)}
    for command in (["init", "-q", "-b", "main"], ["add", "-A"], ["commit", "-q", "-m", "fixture source"]):
        subprocess.run(["git", "-C", str(root), *command], check=True, env=environment, capture_output=True)
    rows = {}
    for name in ("HEAD", "HEAD^{tree}"):
        rows[name] = subprocess.run(["git", "-C", str(root), "rev-parse", name], check=True, env=environment,
                                    capture_output=True, text=True).stdout.strip()
    return rows["HEAD"], rows["HEAD^{tree}"]


def request():
    return json.loads((FIXTURE / "request.json").read_text())


def inputs(root):
    """Copy the release inputs and signed document so a test may replace one of them."""
    shutil.copytree(FIXTURE, root, ignore=shutil.ignore_patterns("mismatch", "previous", "notification.json"))
    return root, (request(), root / "catalog.json", root / "updater.json", root / "packages")


def manifest_sha256(release=FIXTURE):
    return hashlib.sha256((release / "updater.json").read_bytes()).hexdigest()


def distribution_sha256(release=FIXTURE):
    return hashlib.sha256((release / DOCUMENT).read_bytes()).hexdigest()


def notification(path=FIXTURE / "notification.json"):
    return json.loads(path.read_text())


def stored(release=FIXTURE, *, document=None, signature=None):
    """Remote storage objects for one release, keyed as the release workflow writes them."""
    version = json.loads((release / "updater.json").read_text())["version"]
    return {f"releases/v{version}/{DOCUMENT}": (document or release / DOCUMENT).read_bytes(),
            f"releases/v{version}/{SIGNATURE}": (signature or release / SIGNATURE).read_bytes(),
            f"updates/releases/{version}.json": (release / "updater.json").read_bytes()}


@contextmanager
def storage(objects):
    """Serve release storage reads from fixture objects; absent keys read as missing."""
    with patch("scripts.release_distribution.read_storage_bytes", side_effect=lambda key: objects.get(key)):
        yield objects


class Response:
    def __init__(self, raw, headers):
        self.raw, self.headers = raw, headers

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False

    def read(self, limit=-1):
        return self.raw if limit < 0 else self.raw[:limit]


@contextmanager
def web(pages):
    """Serve public HTTPS reads from fixture bytes with uncached response headers."""
    import urllib.error
    import urllib.request

    def urlopen(request, timeout=None):
        url = request.full_url if hasattr(request, "full_url") else request
        if url not in pages:
            raise urllib.error.HTTPError(url, 404, "absent fixture page", {}, None)
        return Response(pages[url], {"Cache-Control": "no-cache, no-store"})

    with patch.object(urllib.request, "urlopen", side_effect=urlopen):
        yield pages


def downloads(release=FIXTURE, *, document=None, signature=None):
    """Public download origin pages for one signed release."""
    version = json.loads((release / "updater.json").read_text())["version"]
    base = f"https://downloads.paintedwolf.dev/releases/v{version}/{DOCUMENT}"
    return {base: (document or release / DOCUMENT).read_bytes(),
            base + ".sig": (signature or release / SIGNATURE).read_bytes()}


@contextmanager
def minisign():
    """Accept exactly the committed document and its committed signature, as minisign would."""
    original = subprocess.run
    accepted = {((release / DOCUMENT).read_bytes(), base64.b64decode((release / SIGNATURE).read_text().strip()))
                for release in (FIXTURE, PREVIOUS)}
    calls = []

    def run(command, *arguments, **keywords):
        if not command or command[0] != "minisign":
            return original(command, *arguments, **keywords)
        message = Path(command[command.index("-m") + 1]).read_bytes()
        signature = Path(command[command.index("-x") + 1]).read_bytes()
        calls.append(hashlib.sha256(message).hexdigest())
        return subprocess.CompletedProcess(command, 0 if (message, signature) in accepted else 1, b"", b"")

    with patch.object(subprocess, "run", side_effect=run):
        yield calls


class FixtureOrigin:
    """Immutable public storage that keeps the bytes it was given and serves them back."""

    def __init__(self, *, mutate=None):
        self.objects, self.mutate = {}, mutate

    def put(self, key, path, content_type, *, expected_identity):
        raw = Path(path).read_bytes()
        if key in self.objects and self.objects[key] != raw:
            raise ValueError("immutable object already holds different bytes")
        if {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)} != expected_identity:
            raise ValueError("upload differs from its admitted identity")
        self.objects[key] = raw
        if self.mutate is not None:
            self.mutate(key)

    def verify(self, key, path, expected):
        raw = self.objects.get(key)
        if raw is None:
            raise ValueError("public object absent")
        if {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)} != expected:
            raise ValueError("public object differs from its admitted identity")
