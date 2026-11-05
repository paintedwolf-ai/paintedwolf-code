"""Compose and sign the selected release's completed distribution through its shared owners."""
from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path
import subprocess

from scripts.linux_release.release_signing import require as qualified
from update_keys import load_registry

from .document import snapshot
from .document_schema import require
from .document_signing import prepare
from .platforms import load_platforms
from .release_facts import DeliveryFacts
from .remote_document import ORIGIN, read_public, trust

ROOT = Path(__file__).resolve().parents[2]


class ReleaseDocument:
    def __init__(self, artifacts, updater, native, nix, source, controller, plan, *, root=ROOT, admission=qualified,
                 read=None, public=read_public, rehearsal=False, origin=ORIGIN,
                 repository_prefix="linux", cache_prefix="nix-cache", signer=None):
        from release_distribution import read_storage_bytes
        require(all(isinstance(value, str) and re.fullmatch("[0-9a-f]{40}", value) for value in (source, controller)),
                "distribution requires exact source and controller commits")
        require(type(rehearsal) is bool, "release trust mode must be explicit")
        require(rehearsal or (origin == ORIGIN and repository_prefix == "linux"
                              and cache_prefix == "nix-cache" and signer is None),
                "production distribution requires maintained trust and delivery targets")
        require(not rehearsal or (origin != ORIGIN and callable(read)),
                "rehearsal requires its own origin and explicit isolated storage transport")
        self.rehearsal, self.signer, self.origin = rehearsal, signer, origin
        self.repository_prefix, self.cache_prefix = repository_prefix, cache_prefix
        self.artifacts, self.updater, self.root = artifacts, updater, root
        self.source, self.controller, self.admission = source, controller, admission
        self.plan = plan
        self.catalog = root / "packaging/release-platforms.json"
        version = (root / "VERSION").read_text().strip()
        from release_semver import parse
        self.version, self.channel = version, parse(version).channel
        self.tree = subprocess.check_output(["git", "-C", str(root), "rev-parse", source + "^{tree}"], text=True).strip()
        self.deliveries = DeliveryFacts(artifacts, native, nix, source, version, self.channel, origin,
                                        read or read_storage_bytes, public, [])

    def facts(self):
        self.admission(self.source)
        require(subprocess.check_output(["git", "-C", str(self.root), "rev-parse", "HEAD"], text=True).strip()
                == self.source, "distribution requires the selected release checkout")
        for name in ("VERSION", "flake.lock", "packaging/release-platforms.json", "packaging/update-keys.json",
                     "packaging/linux/signing/keys.json", "packaging/linux/nix-cache-trust.json",
                     "packaging/distribution-legacy-releases.json", "packaging/distribution-legacy-platforms.json"):
            committed = subprocess.check_output(["git", "-C", str(self.root), "show", self.source + ":" + name])
            require(snapshot(self.root / name)[0]["sha256"] == hashlib.sha256(committed).hexdigest(),
                    "distribution source inputs differ from the selected committed tree")
        from . import public_history
        from release_semver import compare, parse
        history = public_history.load(self.root / "packaging/distribution-legacy-releases.json")
        require(compare(parse(self.version), parse(history["document_release_floor"])) >= 0,
                "distribution document predates its closed legacy publication boundary")
        types = {}
        for platform, row in load_platforms(self.catalog).items():
            if row["publication"] != "public":
                continue
            for extension in row["package_extensions"]:
                name = f"painted-wolf-code_v{self.version}_{platform}.{extension}"
                actual = snapshot(self.artifacts / name)[0]["sha256"]
                require((self.artifacts / (name + ".sha256")).read_text().strip() == actual,
                        "distribution package differs from its collected final checksum")
                types[name] = "application/octet-stream"
        registry = load_registry(self.root / "packaging/update-keys.json")
        trust(self.rehearsal, registry if self.rehearsal else None, self.origin)
        policy_path = self.root / "packaging/linux/nix-cache-trust.json"
        if self.rehearsal:
            cache_policy = json.loads(snapshot(policy_path, retain=True, limit=65536)[1])
        else:
            from scripts.nix_cache.release_lane import trust_policy
            cache_policy = trust_policy(policy_path)
        from .channels import validate, check
        from release_distribution import decode_storage
        selected = validate(json.loads(snapshot(self.plan, retain=True, limit=2 << 20)[1]), self.source, self.version,
                            self.channel, registry["signing_generation"])
        check(selected, read=lambda key: decode_storage(self.deliveries.read(key)))
        self.deliveries.channels = selected["channels"]
        return {"mode": "rehearsal" if self.rehearsal else "production", "version": self.version, "origin": self.origin,
                "source_commit": self.source, "source_tree": self.tree, "controller_commit": self.controller,
                "signing_generation": registry["signing_generation"], "package_content_types": types,
                "repositories": self.deliveries.repositories(catalog=self.root / "packaging/linux/signing/keys.json",
                    platforms=self.catalog, rehearsal=self.rehearsal, prefix=self.repository_prefix),
                "nix": self.deliveries.cache(self.root / "flake.lock", policy=cache_policy,
                    rehearsal=self.rehearsal, prefix=self.cache_prefix)}

    def __call__(self, binding):
        require(binding["catalog"].resolve() == self.catalog.resolve()
                and binding["updater_manifest"].resolve() == self.updater.resolve()
                and binding["artifacts"].resolve() == self.artifacts.resolve(),
                "distribution constructor differs from the selected release inputs")
        require(binding["request"] == self.facts(), "distribution facts changed after release selection")
        return True

    def prepare(self, output):
        return prepare(output.resolve(), self.facts(), self.catalog, self.updater, self.artifacts, authorize=self,
                       registry=load_registry(self.root / "packaging/update-keys.json") if self.rehearsal else None,
                       signer=self.signer)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("artifacts", "updater", "native", "nix", "output", "plan"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--controller", required=True)
    args = parser.parse_args()
    try:
        selected = ReleaseDocument(args.artifacts, args.updater, args.native, args.nix, args.source, args.controller, args.plan)
        print(json.dumps(selected.prepare(args.output), sort_keys=True))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        parser.exit(1, f"Release distribution document failed: {error}\n")


if __name__ == "__main__":
    main()
