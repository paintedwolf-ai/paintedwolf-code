"""Validate cache policy on the final canonical updater response."""
import sys
from pathlib import Path
from email.parser import Parser


def validate(raw: str) -> None:
    blocks = raw.replace("\r\n", "\n").strip().split("\n\n")
    headers = Parser().parsestr(blocks[-1].split("\n", 1)[1])
    tokens = {part.strip().lower() for value in headers.get_all("Cache-Control", []) for part in value.split(",")}
    if "no-store" not in tokens:
        raise ValueError("canonical updater feed must be served with Cache-Control: no-store")
    if headers.get("CF-Cache-Status", "").upper() in {"HIT", "STALE", "UPDATING"} or int(headers.get("Age", "0")) > 0:
        raise ValueError("canonical updater feed was served from a cache")


if __name__ == "__main__":
    validate(Path(sys.argv[1]).read_text())
