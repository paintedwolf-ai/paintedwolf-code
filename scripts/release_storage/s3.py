"""S3 object operations for the isolated, bucket-scoped R2 rehearsal."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def require_environment():
    required = ["R2_S3_ENDPOINT", "R2_BUCKET", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"]
    if any(not os.environ.get(key) for key in required):
        raise ValueError("explicit bucket-scoped S3 credentials and endpoint are required")


def request(operation, *arguments):
    require_environment()
    result = subprocess.run(["aws", "s3api", operation, "--endpoint-url", os.environ["R2_S3_ENDPOINT"],
                             "--bucket", os.environ["R2_BUCKET"], "--output", "json", "--no-cli-pager",
                             *map(str, arguments)], capture_output=True, timeout=1800)
    if result.returncode:
        raise ValueError(f"S3 {operation} failed; check bucket credentials, endpoint and object permissions")
    try:
        return json.loads(result.stdout) if result.stdout.strip() else {}
    except (UnicodeError, json.JSONDecodeError) as error:
        raise ValueError("S3 returned invalid object metadata") from error


def object_stat(key):
    continuation = None
    seen = set()
    while True:
        arguments = ["--prefix", key, "--max-keys", "1000", "--no-paginate"]
        if continuation is not None:
            arguments.extend(["--continuation-token", continuation])
        result = request("list-objects-v2", *arguments)
        if not isinstance(result, dict):
            raise ValueError("S3 listing has an invalid shape")
        rows = result.get("Contents", [])
        if not isinstance(rows, list) or len(rows) > 1000:
            raise ValueError("S3 listing has an invalid shape")
        # A signature can precede its pointer in an R2 prefix listing.
        for row in rows:
            if not isinstance(row, dict):
                raise ValueError("S3 listing has an invalid shape")
            if row.get("Key") == key:
                if type(row.get("Size")) is not int or row["Size"] < 0 or not isinstance(row.get("ETag"), str):
                    raise ValueError("S3 object identity is incomplete")
                return {"key": key, "size": row["Size"], "etag": row["ETag"].strip('"')}
        truncated = result.get("IsTruncated", False)
        if type(truncated) is not bool:
            raise ValueError("S3 listing has an invalid pagination state")
        if not truncated:
            return None
        continuation = result.get("NextContinuationToken")
        if not isinstance(continuation, str) or not continuation or continuation in seen:
            raise ValueError("S3 listing has an invalid continuation token")
        seen.add(continuation)


def get(key, output):
    if object_stat(key) is None:
        return 404
    request("get-object", "--key", key, output)
    return 200


def read_bytes(key, maximum=2 * 1024 * 1024):
    identity = object_stat(key)
    if identity is None:
        return None
    if identity["size"] > maximum:
        raise ValueError("release storage metadata exceeds its bound")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-s3-read-") as temporary:
        path = Path(temporary) / "object"
        metadata = request("get-object", "--key", key, path)
        with path.open("rb") as stream:
            raw = stream.read(maximum + 1)
    if (len(raw) != identity["size"] or len(raw) > maximum
            or metadata.get("ETag", "").strip('"') != identity["etag"]):
        raise ValueError("S3 object changed during the authenticated read")
    return raw


def put(key, source, content_type, cache_control):
    require_environment()
    sha = hashlib.sha256()
    with source.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            sha.update(block)
    # aws s3 cp supplies managed multipart upload for objects larger than a single PUT.
    result = subprocess.run(["aws", "s3", "cp", str(source), f"s3://{os.environ['R2_BUCKET']}/{key}",
        "--endpoint-url", os.environ["R2_S3_ENDPOINT"], "--content-type", content_type,
        "--cache-control", cache_control, "--metadata", "sha256=" + sha.hexdigest(),
        "--only-show-errors", "--no-cli-pager"], capture_output=True, timeout=1800)
    if result.returncode:
        raise ValueError("S3 upload failed; check bucket credentials and object permissions")


def immutable(key, source, content_type, cache_control):
    size = source.stat().st_size
    sha, md5 = hashlib.sha256(), hashlib.md5()
    with source.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            sha.update(block)
            md5.update(block)
    existing = object_stat(key)
    if existing is not None:
        head = request("head-object", "--key", key)
        metadata = head.get("Metadata", {})
        same = metadata.get("sha256") == sha.hexdigest() if "sha256" in metadata else existing["etag"] == md5.hexdigest()
        if head.get("ContentLength") != size or not same:
            raise ValueError("immutable R2 object already exists with different bytes")
        return
    put(key, source, content_type, cache_control)
    head = request("head-object", "--key", key)
    if head.get("ContentLength") != size or head.get("Metadata", {}).get("sha256") != sha.hexdigest():
        raise ValueError("immutable R2 object differs immediately after upload")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["stat", "get", "delete", "put", "immutable"])
    parser.add_argument("key")
    parser.add_argument("file", nargs="?", type=Path)
    parser.add_argument("--content-type", default="application/octet-stream")
    parser.add_argument("--cache-control", default="no-cache, no-store, must-revalidate")
    args = parser.parse_args()
    try:
        if args.operation == "stat":
            result = object_stat(args.key)
            if result is not None:
                print(json.dumps(result))
        elif args.operation == "get":
            if args.file is None:
                parser.error("get requires an output file")
            print(get(args.key, args.file))
        elif args.operation == "delete":
            request("delete-object", "--key", args.key)
        else:
            if args.file is None:
                parser.error("upload requires an input file")
            (immutable if args.operation == "immutable" else put)(
                args.key, args.file, args.content_type, args.cache_control)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        message = str(error) if isinstance(error, ValueError) else "S3 object operation failed"
        parser.exit(1, message + "\n")


if __name__ == "__main__":
    main()
