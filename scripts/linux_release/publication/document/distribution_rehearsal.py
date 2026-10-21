"""Authorize only explicitly pinned, nonpublishing Linux document fixture facts."""
from __future__ import annotations

import copy
import json

FIELDS = {"format_version", "mode", "catalog_sha256", "updater_manifest_sha256",
          "controller_commit", "origin", "version", "notes", "pub_date"}
LINUX = {"linux-x86_64", "linux-aarch64"}


class RehearsalReleaseAuthority:
    """A disposable fixture policy grants no production or hosted availability claims."""

    def __init__(self, policy):
        from scripts.release_distribution_contract.document_schema import digest
        if (not isinstance(policy, dict) or set(policy) != FIELDS
                or type(policy["format_version"]) is not int or policy["format_version"] != 1
                or policy["mode"] != "rehearsal"):
            raise ValueError("distribution rehearsal requires an explicit fixture policy")
        for name in ("catalog_sha256", "updater_manifest_sha256"):
            digest(policy[name])
        digest(policy["controller_commit"], 40)
        self.policy = copy.deepcopy(policy)

    def __call__(self, binding, inputs, report):
        from scripts.release_distribution_contract.document import snapshot
        from scripts.release_distribution_contract.platforms import parse_platforms
        document = binding["document"]
        policy = self.policy
        if (document["mode"] != "rehearsal" or report.get("accepted") is not True
                or any(document[name] != policy[name]
                       for name in ("controller_commit", "origin", "version", "notes", "pub_date"))):
            raise ValueError("distribution fixture differs from its explicit rehearsal selection")
        catalog_identity, catalog_raw = snapshot(binding["catalog"], retain=True, limit=2 << 20)
        updater_identity, updater_raw = snapshot(binding["updater_manifest"], retain=True, limit=2 << 20)
        if (catalog_identity["sha256"] != policy["catalog_sha256"]
                or document["source"]["catalog_sha256"] != policy["catalog_sha256"]
                or updater_identity["sha256"] != policy["updater_manifest_sha256"]
                or document["updater_manifest_sha256"] != policy["updater_manifest_sha256"]):
            raise ValueError("distribution fixture metadata changed after independent selection")
        catalog = parse_platforms(catalog_raw)
        public = {name for name, row in catalog.items() if row["publication"] == "public"}
        updater = json.loads(updater_raw)
        if (public != LINUX or set(document["platforms"]) != LINUX
                or not isinstance(updater, dict) or set(updater) != {"version", "notes", "pub_date", "platforms"}
                or updater["platforms"] != {}
                or any(updater[name] != policy[name] for name in ("version", "notes", "pub_date"))):
            raise ValueError("rehearsal fixture must contain only Linux packages and no updater assets")
        if (document["repositories"] != [] or document["aur"] != {"enabled": False}
                or not isinstance(document["nix"], dict)
                or document["nix"].get("cache") != {"enabled": False}):
            raise ValueError("rehearsal fixture cannot claim repository, AUR or cache availability")
        return True
