"""Retain every published store path; collect only old unreferenced upload debris."""
from __future__ import annotations

import datetime as dt
import json
import re
import uuid

from .r2 import relative_name


def plan(backend, cutoff: dt.datetime) -> list[dict]:
    if cutoff.tzinfo is None or cutoff > dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=1):
        raise ValueError("cache collection requires an explicit cutoff at least one day old")
    rows = backend.listing()
    names = {row["name"] for row in rows}
    retained = set()
    for row in rows:
        name = relative_name(row["name"])
        if name.endswith(".narinfo"):
            lines = backend.read(name).decode("utf-8").splitlines()
            urls = [line[5:] for line in lines if line.startswith("URL: ")]
            if len(urls) != 1 or not urls[0].startswith("nar/"):
                raise ValueError("retained cache metadata lacks its exact NAR reference")
            retained.add(relative_name(urls[0]))
        elif name.startswith("release-records/") and name.endswith(".json"):
            receipt = json.loads(backend.read(name))
            if type(receipt.get("schema_version")) is not int or receipt["schema_version"] != 1 or not isinstance(receipt.get("objects"), list):
                raise ValueError("retained cache release record has an unsupported shape")
            for item in receipt["objects"]:
                retained.add(relative_name(item["name"]))
    if not retained <= names:
        raise ValueError("retained cache closure has missing objects; garbage collection refused")
    # Published narinfo is permanent, including withdrawn releases and old pins.
    # Only never-advertised debris from interrupted uploads can become garbage.
    return [row for row in rows if row["name"] not in retained
            and re.fullmatch(r"nar/[A-Za-z0-9+._-]+\.nar(?:\.(?:xz|zst|bz2|gz))?", row["name"])
            and row["modified"].tzinfo is not None and row["modified"] < cutoff]


def collect(request: dict, cutoff: dt.datetime, *, authorize, backend_factory) -> list[str]:
    if authorize(request, cutoff) is not True:
        raise ValueError("cache collection requires explicit protected controller authority")
    backend = backend_factory(request)
    with backend.serialized(uuid.uuid4().hex):
        garbage = plan(backend, cutoff)
        for row in garbage:
            backend.delete(row)
    return [row["name"] for row in garbage]


def recover(request: dict, previous_operation: str, *, authorize, backend_factory) -> None:
    if authorize(request, previous_operation) is not True:
        raise ValueError("cache lock recovery requires explicit authority for the interrupted operation")
    backend_factory(request).recover_lock(previous_operation)
