"""Authorize native rollback only from independently selected halt identities."""
from pathlib import Path

from .authority import PREFIX, canonical, repository_origin
from .generations import CATALOG
from .repositories import digest


class WithdrawalAuthority:
    """A healthy campaign cannot select the bad generation it replaces."""

    def __init__(self, healthy, *, bad_version, channel, expected, origin):
        from scripts.release_semver import parse
        parse(bad_version)
        if channel not in {"stable", "preview"} or not isinstance(expected, dict) or not expected:
            raise ValueError("native halt requires explicit channel and predecessor selections")
        self.healthy = healthy
        self.bad_version = bad_version
        self.channel = channel
        self.expected = dict(expected)
        self.origin = repository_origin(origin)

    def __call__(self, request):
        required = {"manifest", "directory", "catalog", "storage_prefix", "base_url",
                    "expected", "operation", "phase"}
        if not isinstance(request, dict) or set(request) != required:
            raise ValueError("native halt requires its complete fixed transport request")
        if (request["operation"] != "withdraw" or request["phase"] != "activate"
                or request["storage_prefix"] != PREFIX or request["base_url"] != self.origin
                or Path(request["catalog"]).resolve() != CATALOG.resolve()):
            raise ValueError("native halt differs from protected withdrawal policy")
        inputs, _ = self.healthy.authorize()
        generations = {digest(entry["manifest"]): entry for entry in inputs["repositories"]["generations"]}
        if set(generations) != set(self.expected):
            raise ValueError("native halt must select every healthy repository generation")
        generation = digest(request["manifest"])
        selected = generations.get(generation)
        if (selected is None or canonical(selected["manifest"]) != canonical(request["manifest"])
                or request["manifest"]["channel"] != self.channel
                or request["manifest"]["version"] == self.bad_version
                or request["expected"] != self.expected[generation]
                or Path(request["directory"]).resolve() !=
                (self.healthy.root / "generations" / generation).resolve()):
            raise ValueError("native halt differs from its freshly admitted replacement")
        return True
