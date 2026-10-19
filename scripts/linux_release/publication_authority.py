"""Bind native transport callbacks to freshly admitted controller selections."""

import json
from pathlib import Path
import re
from urllib.parse import urlsplit

from .publication_generations import CATALOG
from .publication_repositories import digest

PREFIX = "linux/repositories"


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False)


def repository_origin(value):
    if not isinstance(value, str):
        raise ValueError("controller repository publication requires a protected HTTPS origin")
    url = urlsplit(value)
    if (url.scheme != "https" or not url.hostname or url.username or url.password
            or url.path not in {"", "/"} or url.query or url.fragment
            or any(character.isspace() for character in value)):
        raise ValueError("controller repository publication requires a protected HTTPS origin")
    return value.rstrip("/")


class RepositoryAuthority:
    """The protected controller independently selects origin and predecessor pins."""

    def __init__(self, selected, *, base_url, expected):
        self.base_url = repository_origin(base_url)
        if (not isinstance(expected, dict) or not expected
                or any(not isinstance(key, str) or not re.fullmatch("[0-9a-f]{64}", key)
                       or (value is not None and (not isinstance(value, str)
                           or not re.fullmatch("[0-9a-f]{64}", value)))
                       for key, value in expected.items())):
            raise ValueError("controller must independently pin every repository predecessor")
        self.selected = selected
        self.expected = dict(expected)

    def __call__(self, request):
        required = {"manifest", "directory", "catalog", "storage_prefix", "base_url",
                    "expected", "operation", "phase"}
        if not isinstance(request, dict) or set(request) != required:
            raise ValueError("repository transport requires its complete selected operation")
        if (request["operation"] != "publish" or request["phase"] not in {"stage", "activate"}
                or request["storage_prefix"] != PREFIX or request["base_url"] != self.base_url
                or Path(request["catalog"]).resolve() != CATALOG.resolve()):
            raise ValueError("repository transport differs from protected publication policy")
        inputs, _ = self.selected.authorize()
        entries = inputs["repositories"]["generations"]
        generations = {digest(entry["manifest"]): entry for entry in entries}
        if set(self.expected) != set(generations):
            raise ValueError("controller predecessor selection differs from the complete campaign")
        identity = digest(request["manifest"])
        entry = generations.get(identity)
        if (entry is None or canonical(entry["manifest"]) != canonical(request["manifest"])
                or request["expected"] != self.expected[identity]
                or entry["operation"].get("previous") != self.expected[identity]
                or Path(request["directory"]).resolve() !=
                (self.selected.root / "generations" / identity).resolve()):
            raise ValueError("repository transport differs from the freshly admitted generation or predecessor")
        return True
