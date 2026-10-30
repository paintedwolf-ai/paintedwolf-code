"""Read completed repository and cache effects for the selected release document."""
from __future__ import annotations

import hashlib
import json

from scripts.linux_release import pacman_publication, release_repositories
from scripts.linux_release.openpgp import selection
from scripts.linux_release.repository_operations import identity
from scripts.linux_release.repository_setup_admission import admit_setup
from scripts.linux_release.repository_setup_identity import package_identity
from scripts.linux_release.signing import CATALOG
from scripts.linux_release.r2_objects import prefix as repository_prefix
from scripts.nix_cache.admission import nar_sha256
from scripts.nix_cache.publication import validate_availability
from scripts.nix_cache.release_lane import SYSTEMS, trust_policy

from .document import snapshot
from .document_schema import canonical, require


def asset(raw, url):
    return {"file": url.rsplit("/", 1)[1], "url": url, "sha256": hashlib.sha256(raw).hexdigest(),
            "bytes": len(raw), "content_type": "application/octet-stream"}


class DeliveryFacts:
    """Current-run receipts select completed effects; live reads must still match them."""

    def __init__(self, artifacts, native, nix, source, version, channel, origin, read, public, channels):
        self.artifacts, self.native, self.nix = artifacts, native, nix
        self.source, self.version, self.channel, self.origin = source, version, channel, origin
        self.read, self.public, self.channels = read, public, channels

    def observed(self, key):
        raw = self.read(key)
        require(isinstance(raw, bytes) and 0 < len(raw) <= 2 << 20, "selected release metadata is unavailable")
        require(self.public(self.origin + "/" + key, 2 << 20) == raw,
                "public metadata differs from authenticated release storage")
        return raw

    def repositories(self, *, catalog=CATALOG, platforms=None, rehearsal=False, prefix="linux"):
        require(type(rehearsal) is bool, "repository trust mode must be explicit")
        repository_prefix(prefix, rehearsal)
        require(rehearsal or prefix == "linux", "production repositories require their maintained namespace")
        rows = json.loads(snapshot(self.native, retain=True, limit=2 << 20)[1])
        require(isinstance(rows, list), "native publication receipt must be an explicit list")
        expected = {(channel, family, arch) for channel in self.channels
                    for family, arch in [("deb", None), ("rpm", None), ("pacman", "x86_64"), ("pacman", "aarch64")]}
        require(len(rows) == len(expected) and {(r["channel"], r["family"], r.get("architecture")) for r in rows} == expected,
                "native publication must cover every release family and architecture")
        signer, selected_key = selection(catalog, rehearsal)
        result = []
        for completed in rows:
            require(completed.get("publication_phase") == "activated" and completed.get("public_verified") is True
                    and completed.get("storage_prefix") == prefix, "native repository publication is incomplete")
            key = prefix + "/" + release_repositories.channel_key(completed).removeprefix("linux/")
            raw = self.observed(key)
            current = json.loads(raw)
            result_fields = {"publication_phase", "public_verified", "storage_prefix"}
            if completed["family"] == "pacman":
                require(completed.get("pair_consistency") == "required-signature-fail-closed-retry",
                        "pacman publication lacks its admitted pair consistency")
                result_fields.add("pair_consistency")
            retained = {key: value for key, value in completed.items() if key not in result_fields}
            require(current == retained and current.get("operation") == "publish",
                    "native channel changed after its selected publication")
            generation_key = prefix + "/generations/" + current["generation"] + "/"
            manifest = json.loads(self.observed(generation_key + "generation.json"))
            require(identity(manifest) == current["generation"], "native generation differs from its selected identity")
            channel = completed["channel"]
            authority = release_repositories.ReleaseAuthority(self.artifacts, self.version, self.source,
                                                              channel, self.origin, None, platforms=platforms, rehearsal=rehearsal, catalog=catalog, storage_prefix=prefix)
            authority.check(manifest, rehearsal=rehearsal)
            require(all(current.get(key) == manifest.get(key) for key in
                        ("family", "channel", "version", "source_commit", "inventory_sha256", "packages",
                         "signing_generation", "key_fingerprint", "rehearsal", "architecture"))
                    and manifest["signing_generation"] == signer["generation"]
                    and manifest["key_fingerprint"] == signer["fingerprint"],
                    "native publication differs from selected source or signing trust")
            public_key = snapshot(self.artifacts / "linux-package-key.asc", retain=True, limit=65536)[1]
            require(public_key == snapshot(selected_key, retain=True, limit=65536)[1]
                    and self.observed("releases/v" + self.version + "/linux-package-key.asc") == public_key,
                    "published package trust differs from the selected setup key")
            if manifest["family"] in {"deb", "rpm"}:
                filename = package_identity(self.version, manifest["family"], channel, signer["generation"])["file"]
                setup = admit_setup(self.artifacts / filename, self.artifacts / (filename + ".SHA256SUMS"),
                    self.artifacts / (filename + ".SHA256SUMS.asc"), catalog,
                    family=manifest["family"], source_commit=self.source, version=self.version, channel=channel,
                    repository_generation=current["generation"], inventory_sha256=manifest["inventory_sha256"],
                    base_url=self.origin + "/" + prefix, signing_generation=signer["generation"],
                    key_fingerprint=signer["fingerprint"], rehearsal=rehearsal)
                require(json.loads(snapshot(self.artifacts / (filename + ".setup.json"), retain=True, limit=2 << 20)[1]) == setup,
                        "retained setup receipt differs from independently admitted package bytes")
                setup_raw = snapshot(self.artifacts / filename, retain=True, limit=2 << 20)[1]
                setup_url = self.origin + "/releases/v" + self.version + "/" + filename
                require(self.observed(setup_url.removeprefix(self.origin + "/")) == setup_raw,
                        "published setup differs from its admitted archive")
            else:
                setup_raw = self.observed(generation_key + "client.conf")
                expected_config = pacman_publication.configuration(manifest, self.origin + "/" + key.removesuffix("/current.json"),
                                                                    signer["fingerprint"]).encode()
                require(setup_raw == expected_config, "pacman setup differs from its selected generation")
                setup_url = self.origin + "/" + generation_key + "client.conf"
            result.append({"family": manifest["family"], "architecture": manifest.get("architecture", "both"),
                "channel": channel, "generation": current["generation"],
                "inventory_sha256": manifest["inventory_sha256"], "activation_sha256": hashlib.sha256(raw).hexdigest(),
                "base_url": self.origin + "/" + key.removesuffix("/current.json"),
                "signing_generation": signer["generation"], "key_fingerprint": signer["fingerprint"],
                "setup": asset(setup_raw, setup_url),
                "public_key": asset(public_key, self.origin + "/releases/v" + self.version + "/linux-package-key.asc")})
        return result

    def cache(self, lock, *, policy=None, rehearsal=False, prefix="nix-cache"):
        require(type(rehearsal) is bool, "cache trust mode must be explicit")
        receipts = json.loads(snapshot(self.nix, retain=True, limit=2 << 20)[1])
        require(isinstance(receipts, list) and len(receipts) == len(SYSTEMS)
                and {r["request"]["system"] for r in receipts} == set(SYSTEMS),
                "cache publication must cover both native systems")
        policy = trust_policy() if policy is None else policy
        closure_rows, identities = [], []
        lock_sha = snapshot(lock)[0]["sha256"]
        for receipt in receipts:
            validate_availability(receipt, policy)
            request = receipt["request"]
            require(request["mode"] == ("rehearsal" if rehearsal else "production")
                    and request["application_commit"] == self.source
                    and request["version"] == self.version and request["flake_lock_sha256"] == lock_sha
                    and request["cache_prefix"] == prefix,
                    "cache publication differs from selected source or trust")
            key = prefix + "/release-records/" + self.version + "/" + request["system"] + ".json"
            require(self.read(key + ".withdrawn") is None, "withdrawn cache cannot be advertised")
            raw = self.observed(key)
            require(json.loads(raw) == receipt, "cache publication changed after the selected release")
            identities.append(hashlib.sha256(raw).hexdigest())
            require(len(request["outputs"]) == 1, "release cache must select one application output per system")
            output = request["outputs"][0]
            closure_rows.append({"system": request["system"], "store_path": output,
                                 "nar_sha256": nar_sha256(receipt["closure"][output]["nar_hash"])})
        return {"source": {"git_url": "github:paintedwolf-ai/paintedwolf-code/" + self.source,
                           "commit": self.source, "flake_lock_sha256": lock_sha},
                "cache": {"enabled": True, "base_url": self.origin + "/" + prefix, "public_keys": policy["public_keys"],
                          "admission_sha256": hashlib.sha256(canonical(identities)).hexdigest(), "closures": closure_rows}}
