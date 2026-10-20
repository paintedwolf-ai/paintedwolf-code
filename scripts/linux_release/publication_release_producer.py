"""Retain independently signed native release results without publication."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess

from .installed_provenance import controller_context
from .publication_release_inputs import SelectedReleaseInputs
from .release_source import facts


def produce(source_repository, source_commit, catalog, updater, artifacts, output, producer):
    from scripts.release_distribution_contract.document import snapshot
    from scripts.release_distribution_contract.platforms import parse_platforms
    source = facts(source_repository, source_commit)
    if (output.exists() or output.is_symlink() or any(output.resolve().is_relative_to(path.resolve())
            for path in (source_repository, artifacts))):
        raise ValueError("release input retention requires a fresh independent output")
    _, raw_catalog = snapshot(catalog, retain=True, limit=2 << 20)
    committed = subprocess.check_output(["git", "--no-replace-objects", "-C", str(source_repository),
        "show", source_commit + ":packaging/release-platforms.json"], timeout=60)
    if raw_catalog != committed:
        raise ValueError("release catalog differs from its selected immutable application source")
    _, raw_updater = snapshot(updater, retain=True, limit=2 << 20)
    platforms = parse_platforms(raw_catalog)
    names = set()
    for key, row in platforms.items():
        if row["publication"] != "public" or key.startswith("linux-"):
            continue
        stem = f"painted-wolf-code_v{source['version']}_{key}"
        for extension in row["package_extensions"]:
            names.update((stem + "." + extension, stem + "." + extension + ".sha256"))
        extension = row["updater_extension"]
        if extension is None:
            raise ValueError("public non-Linux release requires its actual signed updater")
        names.update((stem + "." + extension, stem + "." + extension + ".sig"))
    if not names:
        raise ValueError("independent native release inputs cannot be an empty fixture")
    identities = {name: snapshot(artifacts / name)[0] for name in names}
    output.mkdir(mode=0o700)
    try:
        copied = output / "artifacts"
        copied.mkdir(mode=0o700)
        for name, identity in identities.items():
            shutil.copyfile(artifacts / name, copied / name)
            (copied / name).chmod(0o600)
            if snapshot(copied / name)[0] != identity or snapshot(artifacts / name)[0] != identity:
                raise ValueError("signed native result changed during release retention")
        for name, raw in (("catalog.json", raw_catalog), ("updater.json", raw_updater)):
            with (output / name).open("xb") as stream:
                stream.write(raw)
        proof = {"format_version": 1, **producer, "source_commit": source_commit, "source_tree": source["source_tree"]}
        with (output / "producer.json").open("x") as stream:
            json.dump(proof, stream, indent=2, sort_keys=True)
            stream.write("\n")
        # This checks copied sets/hashes/metadata. Native signature execution is
        # supplied by the protected workflow's actual build/audit dependencies.
        SelectedReleaseInputs(output, producer, source, producer["controller_commit"])
        if snapshot(catalog, retain=True, limit=2 << 20)[1] != raw_catalog or snapshot(updater, retain=True, limit=2 << 20)[1] != raw_updater:
            raise ValueError("release metadata changed while retaining signed native results")
        return {"format_version": 1, "published": False, "source": source,
                "retained_files": len(identities), "producer": producer}
    except BaseException:
        shutil.rmtree(output)
        raise


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("source-repository", "catalog", "updater", "artifacts", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args(arguments)
    try:
        producer = controller_context(os.environ)
        produce(args.source_repository, args.source_commit, args.catalog, args.updater, args.artifacts, args.output, producer)
        print(f"Protected native release inputs retained without publication: {args.output}")
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, "Release input retention refused: " + str(error) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
