"""Nonpublishing Nix signature and closure rehearsal with disposable local stores."""
from __future__ import annotations

import json
from pathlib import Path
import shutil

from .adapter import construct, nix, path_info, verify
from .admission import inventory, validate_request
from .rehearsal_fixture import cache_fixture, local_store


def must_refuse(operation, label: str) -> None:
    try:
        operation()
    except ValueError:
        return
    raise ValueError(f"Nix cache rehearsal accepted {label}")


def main() -> None:
    from .rehearsal_transport import run as rehearse_transport
    with cache_fixture() as fixture:
        work, system, source = fixture.work, fixture.system, fixture.source
        output, keys, files = fixture.output, fixture.keys, fixture.key_files
        cache, report = fixture.cache, fixture.report
        request = report["request"]
        policy = {"schema_version": 1, "mode": "rehearsal", "public_keys": [keys[0]]}
        wrong_system = "aarch64-linux" if system == "x86_64-linux" else "x86_64-linux"
        must_refuse(lambda: construct({**request, "system": wrong_system}, policy, files[0], work / "wrong-system", source_store=source), "a mislabeled architecture")
        if len(report["closure"]) < 2 or not report["closure"][output]["references"]:
            raise ValueError("Nix cache rehearsal did not construct a dependent closure")
        must_refuse(lambda: verify(cache, [output], [keys[1]]), "a wrong signature key")
        cold = local_store(work / "cold")
        nix("copy", "--from", cache.as_uri(), "--to", cold, "--trusted-public-keys", keys[0],
            "--option", "extra-trusted-public-keys", "", "--stdin", stdin=output + "\n")
        if set(path_info(cold, [output])) != set(report["closure"]):
            raise ValueError("Nix cold store did not receive the complete closure")
        must_refuse(lambda: nix("copy", "--from", cache.as_uri(), "--to", local_store(work / "wrong-key-cold"),
                               "--trusted-public-keys", keys[1], "--option", "extra-trusted-public-keys", "",
                               "--stdin", stdin=output + "\n"), "cold substitution with a wrong key")
        rotated = work / "rotated"
        shutil.copytree(cache, rotated)
        nix("store", "sign", "--store", rotated.as_uri(), "--key-file", str(files[1]), "--recursive", "--stdin", stdin=output + "\n")
        verify(rotated, [output], keys)
        verify(rotated, [output], [keys[1]])
        inventory(rotated, report["closure"], [keys[1]])
        transport_checks = rehearse_transport(cache, report, rotated, keys)
        for label in ("altered-nar", "altered-metadata", "missing-nar", "missing-dependency"):
            broken = work / label
            shutil.copytree(cache, broken)
            nar = next(row["name"] for row in report["objects"] if row["phase"] == "nar")
            metadata = Path(output).name.split("-", 1)[0] + ".narinfo"
            if label == "altered-nar":
                (broken / nar).write_bytes(b"invalid NAR")
            elif label == "altered-metadata":
                path = broken / metadata
                path.write_text(path.read_text().replace("NarSize: ", "NarSize: 1", 1))
            elif label == "missing-nar":
                (broken / nar).unlink()
            else:
                dependency = next(path for path in report["closure"] if path != output)
                (broken / (Path(dependency).name.split("-", 1)[0] + ".narinfo")).unlink()
            must_refuse(lambda: verify(broken, [output], [keys[0]]), label)
        production = {**request, "mode": "production", "cache_prefix": "nix-cache"}
        must_refuse(lambda: validate_request(production, policy), "rehearsal policy in production")
        must_refuse(lambda: validate_request(production, {**policy, "mode": "production"}), "rehearsal key identity in production")
        print(json.dumps({"schema_version": 1, "mode": "rehearsal", "status": "passed", "nix_version": report["nix_version"],
                          "system": system, "closure_paths": len(report["closure"]), "objects": len(report["objects"]),
                          "cold_copy": True, "checks": ["wrong-key", "wrong-key-cold-copy", "rotation-overlap", "rotation-new-key",
                                                          "altered-nar", "altered-metadata", "missing-nar", "missing-dependency", "wrong-system", "production-refusal", *transport_checks]}, sort_keys=True))


if __name__ == "__main__":
    main()
