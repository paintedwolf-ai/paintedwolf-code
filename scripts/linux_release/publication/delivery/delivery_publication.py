"""Stage complete admitted delivery before activating selected native repository channels."""
from pathlib import Path
import os
import stat

from ..trust.authority import PREFIX, RepositoryAuthority
from ..trust.generations import CATALOG
from .immutable import ImmutableOrigin, publish_setups
from ..trust.repositories import digest
from ...release_admission import document


def publish(selected, *, origin, expected, immutable_backend=None):
    """The caller independently pins predecessors; retained inputs cannot grant that choice."""
    authority = RepositoryAuthority(selected, base_url=origin, expected=expected)
    inputs, _ = selected.authorize()
    entries = inputs["repositories"]["generations"]
    if set(expected) != {digest(entry["manifest"]) for entry in entries}:
        raise ValueError("delivery publication must select every independently admitted generation")
    results = []
    for phase in ("stage", "activate"):
        if phase == "activate":
            publish_setups(selected, backend=immutable_backend or ImmutableOrigin())
            publish_caches(selected)
        for entry in entries:
            manifest = entry["manifest"]
            generation = digest(manifest)
            if manifest["family"] == "pacman":
                from ...pacman_r2 import publish_r2
            else:
                from ...repository_r2 import publish_r2
            current, _ = selected.authorize()
            if current != inputs:
                raise ValueError("delivery inputs changed between stage and activation")
            results.append(publish_r2(manifest, selected.root / "generations" / generation, CATALOG,
                PREFIX, origin, expected[generation], False, "publish", phase, authorize=authority))
    selected.authorize()
    return results


def publish_caches(selected):
    from ..trust.cache_policy import POLICY, load
    policy = load(POLICY)
    if policy is None:
        return []
    from scripts.nix_cache.publication import publish as publish_cache
    from scripts.nix_cache.r2 import R2Transport
    from scripts.nix_cache.release_authority import authorize
    reports = []
    for system in ("aarch64-linux", "x86_64-linux"):
        directory = selected.root / "nix-cache" / system
        report, _ = document(directory / "report.json")
        def admission(request, checked):
            inputs, _ = selected.authorize()
            return authorize(inputs, request, checked, trust_policy=policy)
        reports.append(publish_cache(directory / "cache", report, authorize=admission, backend_factory=R2Transport))
    return reports


def load_policy(path):
    path = Path(path).absolute()
    info = path.lstat()
    if (path.resolve(strict=True) != path or not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022
            or info.st_uid not in {0, os.geteuid()} or info.st_size > 1 << 20):
        raise ValueError("delivery policy must be a bounded physical owner-controlled file")
    policy, _ = document(path)
    if (not isinstance(policy, dict) or set(policy) != {"format_version", "expected"}
            or type(policy["format_version"]) is not int or policy["format_version"] != 1):
        raise ValueError("delivery policy requires explicit independently selected predecessors")
    return policy["expected"]
