"""Load private rehearsal configuration without evaluating shell code."""

import json
import os
from pathlib import Path
import re
import stat
import sys
from urllib.parse import urlsplit

DEFAULT_CONFIG = Path.home() / ".config/paintedwolf/release-rehearsal.json"
FIELDS = {"endpoint_url", "bucket", "access_key_id", "secret_access_key", "download_base_url"}
ENVIRONMENT = {"endpoint_url": "RELEASE_TEST_R2_ENDPOINT_URL", "bucket": "RELEASE_TEST_R2_BUCKET",
               "access_key_id": "RELEASE_TEST_R2_ACCESS_KEY_ID", "secret_access_key": "RELEASE_TEST_R2_SECRET_ACCESS_KEY",
               "download_base_url": "RELEASE_TEST_DOWNLOAD_BASE_URL", "session_token": "RELEASE_TEST_R2_SESSION_TOKEN"}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate configuration field")
        result[key] = value
    return result


def load_file(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        before = os.fstat(stream.fileno())
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                or stat.S_IMODE(before.st_mode) not in {0o600, 0o400} or before.st_size > 65536):
            raise ValueError("configuration must be an owned regular file with mode 600 or 400")
        raw = stream.read(65537)
        after = os.fstat(stream.fileno())
        identity = lambda row: (row.st_dev, row.st_ino, row.st_size, row.st_mtime_ns, row.st_ctime_ns)
        if len(raw) != before.st_size or identity(before) != identity(after):
            raise ValueError("configuration changed while reading")
    try:
        return json.loads(raw, object_pairs_hook=unique_object)
    except (UnicodeError, json.JSONDecodeError) as error:
        raise ValueError("configuration must contain valid JSON") from error


def validate(config):
    if (not isinstance(config, dict) or set(config) - (FIELDS | {"schema_version", "session_token"})
            or not FIELDS <= set(config) or type(config.get("schema_version")) is not int
            or config["schema_version"] != 1):
        raise ValueError("configuration requires schema_version 1 and the documented S3 fields")
    for field in FIELDS | ({"session_token"} if "session_token" in config else set()):
        value = config[field]
        if not isinstance(value, str) or not value or any(character.isspace() for character in value):
            raise ValueError(f"configuration field {field} must be a nonempty string without whitespace")
    if not re.fullmatch(r"https://[0-9a-f]{32}(?:\.(?:eu|us|fedramp))?\.r2\.cloudflarestorage\.com/?", config["endpoint_url"]):
        raise ValueError("endpoint_url must be the HTTPS R2 S3 endpoint for the bucket jurisdiction")
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", config["bucket"]) or ".." in config["bucket"]:
        raise ValueError("bucket must be a valid S3 bucket name")
    url = urlsplit(config["download_base_url"])
    if (url.scheme != "https" or not url.hostname or url.username or url.password
            or url.path not in {"", "/"} or url.query or url.fragment):
        raise ValueError("download_base_url must be an HTTPS origin without credentials, path, query or fragment")
    return config


def environment(config, inherited):
    config = validate(config)
    # Explicit credentials cannot fall back to a profile, role, metadata service or production token.
    result = {key: value for key, value in inherited.items()
              if not key.startswith(("AWS_", "CLOUDFLARE_", "RELEASE_TEST_R2_"))
              and key not in {"SHELLOPTS", "BASHOPTS", "BASH_ENV", "ENV"}}
    result.update(AWS_ACCESS_KEY_ID=config["access_key_id"], AWS_SECRET_ACCESS_KEY=config["secret_access_key"],
                  AWS_DEFAULT_REGION="auto", AWS_EC2_METADATA_DISABLED="true", AWS_PAGER="",
                  AWS_CONFIG_FILE=os.devnull, AWS_SHARED_CREDENTIALS_FILE=os.devnull, AWS_CLI_AUTO_PROMPT="off",
                  AWS_REQUEST_CHECKSUM_CALCULATION="when_required", AWS_RESPONSE_CHECKSUM_VALIDATION="when_required",
                  R2_S3_ENDPOINT=config["endpoint_url"].rstrip("/"), R2_BUCKET=config["bucket"],
                  RELEASE_TEST_DOWNLOAD_BASE_URL=config["download_base_url"].rstrip("/"),
                  RELEASE_R2_TRANSPORT="s3", PW_R2_REHEARSAL_CONFIGURED="1")
    if "session_token" in config:
        result["AWS_SESSION_TOKEN"] = config["session_token"]
    return result


def configuration(inherited):
    selected = inherited.get("RELEASE_TEST_CONFIG")
    if selected or DEFAULT_CONFIG.exists():
        return load_file(Path(selected).expanduser() if selected else DEFAULT_CONFIG)
    return {"schema_version": 1, **{field: inherited[name] for field, name in ENVIRONMENT.items()
                                   if inherited.get(name)}}


def main():
    try:
        configured = environment(configuration(os.environ), os.environ)
        if configured.get("GITHUB_ACTIONS") == "true":
            for key in ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"]:
                if configured.get(key):
                    print("::add-mask::" + configured[key], flush=True)
        os.execvpe("bash", ["bash", sys.argv[1], *sys.argv[2:]], configured)
    except (OSError, ValueError) as error:
        # JSON parser errors and failed reads must never echo credential contents.
        message = str(error) if isinstance(error, ValueError) else "cannot read private rehearsal configuration"
        sys.exit("R2 rehearsal configuration refused: " + message)


if __name__ == "__main__":
    main()
