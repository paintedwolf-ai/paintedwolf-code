"""Build and admit repository setup packages before the shared release publishes artifacts."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

from . import repository_setup, repository_setup_admission
from .openpgp import selection
from .release_repositories import ReleaseAuthority, public_linux
from .release_signing import private_key
from .repository_objects import encoded
from .repository_operations import identity
from .signing import CATALOG


def assemble(generations: Path, artifacts: Path, output: Path, version: str, source_commit: str,
             channels: list[str], base_url: str, private: Path, catalog: Path = CATALOG, *, rehearsal: bool = False):
    """Use the selected signed generations, then reconstruct each setup's authority before collection."""
    if type(rehearsal) is not bool:
        raise ValueError("setup trust mode must be explicit")
    signer, public = selection(catalog, rehearsal)
    public_bytes = public.read_bytes()
    receipts = []
    with tempfile.TemporaryDirectory(prefix="paintedwolf-release-setup-") as temporary:
        prepared = Path(temporary)
        for channel in channels:
            authority = ReleaseAuthority(artifacts, version, source_commit, channel, base_url, None)
            for family in ("deb", "rpm"):
                directory = generations / channel / family
                manifest = json.loads((directory / "generation.json").read_bytes())
                if manifest.get("family") != family:
                    raise ValueError("setup generation differs from its selected native family")
                authority.check(manifest, rehearsal=rehearsal)
                destination = prepared / (channel + "-" + family)
                built = repository_setup.build(manifest, directory, catalog, destination, base_url, private, rehearsal)
                filename = built["package"]["file"]
                admitted = repository_setup_admission.admit_setup(
                    destination / filename, destination / "SHA256SUMS", destination / "SHA256SUMS.asc", catalog,
                    family=family, source_commit=source_commit, version=version, channel=channel,
                    repository_generation=identity(manifest), inventory_sha256=manifest["inventory_sha256"],
                    base_url=base_url, signing_generation=signer["generation"], key_fingerprint=signer["fingerprint"],
                    rehearsal=rehearsal)
                if admitted != built or (destination / "package-key.asc").read_bytes() != public_bytes:
                    raise ValueError("setup package differs from its independently reconstructed release authority")
                receipts.append(admitted)
        output.mkdir(parents=True, exist_ok=False)
        for receipt in receipts:
            filename = receipt["package"]["file"]
            destination = prepared / (receipt["channel"] + "-" + receipt["family"])
            for original, published in ((filename, filename), ("SHA256SUMS", filename + ".SHA256SUMS"),
                                        ("SHA256SUMS.asc", filename + ".SHA256SUMS.asc")):
                shutil.copyfile(destination / original, output / published)
            (output / (filename + ".setup.json")).write_bytes(encoded(receipt))
        (output / "linux-package-key.asc").write_bytes(public_bytes)
    return receipts


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("generations", "artifacts", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    for name in ("version", "source-commit", "channel", "base-url"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--plan", type=Path, required=True)
    args = parser.parse_args()
    try:
        if not public_linux():
            raise ValueError("repository setup requires public Linux release platforms")
        from release_distribution_contract.channels import validate
        from update_keys import load_registry
        selected = validate(json.loads(args.plan.read_bytes()), args.source_commit, args.version, args.channel,
                            load_registry()["signing_generation"])
        with private_key() as private:
            receipts = assemble(args.generations, args.artifacts, args.output, args.version, args.source_commit,
                                selected["channels"], args.base_url.rstrip("/"), private)
        print(json.dumps(receipts, sort_keys=True))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux release repository setup failed: {exc}\n")


if __name__ == "__main__":
    main()
