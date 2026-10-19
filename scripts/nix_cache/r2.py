"""Dedicated-prefix R2 transport with conditional locking and ordinary HTTPS reads."""
from __future__ import annotations

from contextlib import contextmanager
import datetime as dt
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
LOCK = "publication-lock.json"


def relative_name(value: str) -> str:
    if not isinstance(value, str):
        raise ValueError("cache transport key must be a string")
    path = PurePosixPath(value)
    if not re.fullmatch(r"[A-Za-z0-9+._/-]+", value) or path.is_absolute() or str(path) != value or ".." in path.parts:
        raise ValueError("cache transport key must be a normalized relative name")
    return value


def digest_file(path: Path) -> tuple[int, str]:
    digest, size = hashlib.sha256(), 0
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
            size += len(chunk)
    return size, digest.hexdigest()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("cache public verification cannot follow redirects")


class R2Transport:
    def __init__(self, request: dict):
        self.prefix = request["cache_prefix"]
        if self.prefix != "nix-cache" and not re.fullmatch(r"release-system-tests/[A-Za-z0-9_-]+/nix-cache", self.prefix):
            raise ValueError("R2 cache requires its dedicated production or rehearsal prefix")
        self.bucket = os.environ["R2_BUCKET"]
        self.account = os.environ["CLOUDFLARE_ACCOUNT_ID"]
        if not re.fullmatch(r"[a-f0-9]{32}", self.account) or not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", self.bucket):
            raise ValueError("cache storage account or bucket is invalid")
        self.public = os.environ["DOWNLOAD_BASE_URL"].rstrip("/") + "/" + self.prefix
        if request["mode"] == "production" and self.public != "https://downloads.paintedwolf.dev/nix-cache":
            raise ValueError("production cache delivery must use the selected product domain")
        parsed = urllib.parse.urlsplit(self.public)
        if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError("cache delivery requires an explicit HTTPS domain")
        # Reuse the repository's token-to-S3 authority rather than inventing credentials.
        script = 'source "$1"; r2_s3_require_env; python3 -c \'import json,os; print(json.dumps({k:v for k,v in os.environ.items() if k.startswith("AWS_")}))\''
        result = subprocess.run(["bash", "-euc", script, "nix-cache", str(ROOT / "scripts/release-r2-rest.sh")],
                                capture_output=True, text=True, timeout=120)
        if result.returncode:
            raise ValueError("cache R2 authority initialization failed")
        selected = {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION",
                    "AWS_REQUEST_CHECKSUM_CALCULATION", "AWS_RESPONSE_CHECKSUM_VALIDATION"}
        credentials = {key: value for key, value in json.loads(result.stdout.splitlines()[-1]).items() if key in selected}
        self.environment = {key: value for key, value in os.environ.items() if not key.startswith("AWS_")}
        self.environment.update(credentials, AWS_CONFIG_FILE="/dev/null", AWS_SHARED_CREDENTIALS_FILE="/dev/null",
                                AWS_PAGER="", AWS_EC2_METADATA_DISABLED="true")
        self.endpoint = f"https://{self.account}.r2.cloudflarestorage.com"

    def _aws(self, *arguments) -> str:
        result = subprocess.run(["aws", "s3api", *arguments, "--bucket", self.bucket, "--endpoint-url", self.endpoint],
                                capture_output=True, text=True, env=self.environment, timeout=1800)
        if result.returncode:
            raise ValueError("cache R2 operation failed; retained objects and lock require inspection")
        return result.stdout

    def _key(self, name: str) -> str:
        return self.prefix + "/" + relative_name(name)

    def listing(self, prefix: str = "") -> list[dict]:
        rows, token, seen = [], None, set()
        while True:
            arguments = ["list-objects-v2", "--prefix", self.prefix + "/" + prefix, "--max-keys", "1000", "--no-paginate"]
            if token:
                arguments += ["--continuation-token", token]
            page = json.loads(self._aws(*arguments))
            for item in page.get("Contents", []):
                name = item["Key"].removeprefix(self.prefix + "/")
                relative_name(name)
                if name in seen or item["Key"] != self._key(name):
                    raise ValueError("cache listing contains duplicate or foreign objects")
                seen.add(name)
                rows.append({"name": name, "bytes": item["Size"], "etag": item["ETag"],
                             "modified": dt.datetime.fromisoformat(item["LastModified"].replace("Z", "+00:00"))})
            if not page.get("IsTruncated", False):
                return rows
            next_token = page.get("NextContinuationToken")
            if not next_token or next_token == token:
                raise ValueError("cache listing did not advance")
            token = next_token

    def _read(self, name: str) -> tuple[bytes, str] | None:
        relative_name(name)
        rows = [row for row in self.listing(name) if row["name"] == name]
        if not rows:
            return None
        if rows[0]["bytes"] > 64 << 20:
            raise ValueError("cache metadata exceeds its read bound")
        with tempfile.TemporaryDirectory(prefix="nix-cache-read-") as directory:
            path = Path(directory) / "object"
            result = json.loads(self._aws("get-object", "--key", self._key(name), str(path), "--if-match", rows[0]["etag"]))
            if path.stat().st_size != rows[0]["bytes"]:
                raise ValueError("cache object changed while reading")
            return path.read_bytes(), result["ETag"]

    def read(self, name: str) -> bytes | None:
        result = self._read(name)
        return result[0] if result is not None else None

    def _write(self, name: str, path: Path, content_type: str, cache_control: str, *conditions):
        return self._aws("put-object", "--key", self._key(name), "--body", str(path), "--content-type", content_type,
                         "--cache-control", cache_control, *conditions)

    def put_bytes(self, name: str, data: bytes, content_type: str, cache_control: str):
        previous = self.read(name)
        if previous is not None:
            if previous != data:
                raise ValueError("immutable cache release record already differs")
            return
        with tempfile.TemporaryDirectory(prefix="nix-cache-write-") as directory:
            path = Path(directory) / "object"
            path.write_bytes(data)
            self._write(name, path, content_type, cache_control, "--if-none-match", "*")
        if self.read(name) != data:
            raise ValueError("cache release record differs after publication")

    def put(self, name: str, path: Path, record: dict):
        if digest_file(path) != (record["bytes"], record["sha256"]):
            raise ValueError("cache source changed before upload")
        if record["phase"] != "nar":
            self.put_bytes(name, path.read_bytes(), record["content_type"], record["cache_control"])
            return
        result = subprocess.run(["bash", str(ROOT / "scripts/release-r2-immutable-put.sh"), "--key", self._key(name),
                                 "--file", str(path), "--content-type", record["content_type"], "--cache-control", record["cache_control"]],
                                capture_output=True, env=self.environment, timeout=1800)
        if result.returncode:
            raise ValueError("immutable cache upload failed")

    def replace(self, name: str, path: Path, record: dict, previous: bytes):
        if digest_file(path) != (record["bytes"], record["sha256"]):
            raise ValueError("cache metadata changed before upload")
        observed = self._read(name)
        if observed is None or observed[0] != previous:
            raise ValueError("cache metadata changed since rotation was admitted")
        self._write(name, path, record["content_type"], record["cache_control"], "--if-match", observed[1])

    def verify_public(self, name: str, record: dict):
        request = urllib.request.Request(self.public + "/" + relative_name(name), headers={"User-Agent": "painted-wolf-release/1"})
        try:
            with urllib.request.build_opener(NoRedirect).open(request, timeout=120) as response:
                control = {part.strip().lower() for part in response.headers.get("Cache-Control", "").split(",")}
                if record["phase"] == "nar" and not {"public", "max-age=31536000", "immutable"} <= control:
                    raise ValueError("cache NAR delivery lacks immutable content caching")
                if record["phase"] != "nar" and ("no-store" not in control or response.headers.get("Age", "0") != "0"
                                                  or response.headers.get("CF-Cache-Status", "").lower() in {"hit", "stale", "updating"}):
                    raise ValueError("cache metadata delivery permits stale or negative caching")
                digest, size = hashlib.sha256(), 0
                while chunk := response.read(1024 * 1024):
                    size += len(chunk)
                    if size > record["bytes"]:
                        raise ValueError("cache public object exceeds its admitted size")
                    digest.update(chunk)
                if response.status != 200 or (size, digest.hexdigest()) != (record["bytes"], record["sha256"]):
                    raise ValueError("cache public delivery differs from its admitted object")
        except urllib.error.HTTPError as error:
            raise ValueError("cache public delivery is unavailable; publication remains incomplete") from error

    @contextmanager
    def serialized(self, operation: str):
        if not re.fullmatch(r"[a-f0-9]{32}", operation):
            raise ValueError("cache write operation must have a unique identifier")
        payload = json.dumps({"schema_version": 1, "operation": operation}, sort_keys=True).encode()
        free = b'{"schema_version": 1, "state": "free"}'
        with tempfile.TemporaryDirectory(prefix="nix-cache-lock-") as directory:
            path = Path(directory) / "lock"
            path.write_bytes(payload)
            previous = self._read(LOCK)
            if previous is None:
                condition = ["--if-none-match", "*"]
            elif previous[0] == free:
                condition = ["--if-match", previous[1]]
            else:
                raise ValueError("cache publisher is locked; explicit interrupted-operation recovery is required")
            result = json.loads(self._write(LOCK, path, "application/json", "no-store", *condition))
            try:
                yield self
            finally:
                path.write_bytes(free)
                self._write(LOCK, path, "application/json", "no-store", "--if-match", result["ETag"])

    def recover_lock(self, previous_operation: str):
        if not re.fullmatch(r"[a-f0-9]{32}", previous_operation):
            raise ValueError("cache lock recovery requires its exact previous operation")
        previous = self._read(LOCK)
        if previous is None or previous[0] != json.dumps({"schema_version": 1, "operation": previous_operation}, sort_keys=True).encode():
            raise ValueError("cache lock no longer belongs to the selected interrupted operation")
        with tempfile.TemporaryDirectory(prefix="nix-cache-recovery-") as directory:
            path = Path(directory) / "lock"
            path.write_bytes(b'{"schema_version": 1, "state": "free"}')
            self._write(LOCK, path, "application/json", "no-store", "--if-match", previous[1])

    def delete(self, row: dict):
        current = [item for item in self.listing(row["name"]) if item["name"] == row["name"]]
        if current != [row]:
            raise ValueError("cache garbage object changed after its locked inventory")
        self._aws("delete-object", "--key", self._key(row["name"]))
