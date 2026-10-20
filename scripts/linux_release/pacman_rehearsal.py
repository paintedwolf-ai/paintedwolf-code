"""Rehearse native makepkg and signed pacman repository formats without publication."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import uuid

from .openpgp import ephemeral, keyring, selection, run
from .pacman_repository import assemble, package_info, binary_signature
from .aur_recipe import generate
from .aur_delivery import FixturePublisher, plan as aur_delivery_plan
from .pacman_validation import admit
from .pacman_publication import publish
from .pacman_r2 import publish_r2
from .r2_objects import rehearsal_environment, warm_absence, tamper_fixture_object, delete_fixture_object, put, public_bytes
from .repository_objects import replace, encoded
from .signing import digest
from .yay_rehearsal import install as install_yay

PACKAGE = "paintedwolf-repository-fixture"


def rejected(action):
    try:
        action()
    except (ValueError, OSError, KeyError):
        return
    raise ValueError("invalid pacman generation was admitted")


def install(package: Path, root: Path, catalog: Path, expected: str, repository: Path | str | None = None):
    root.mkdir(exist_ok=True)
    for name in ["db", "cache", "hooks"]:
        (root / name).mkdir(exist_ok=True)
    config = root / "pacman.conf"
    config.write_text("[options]\nArchitecture = " + platform.machine()
                      + "\nSigLevel = Required DatabaseRequired\nLocalFileSigLevel = Required\n")
    if repository is not None:
        with config.open("a") as output:
            url = repository.as_uri() if isinstance(repository, Path) else repository
            output.write("\n[painted-wolf-code-preview]\nServer = " + url + "\n")
    selected, public = selection(catalog, True)
    with keyring(public, selected["fingerprint"]) as home:
        run(["gpg", "--batch", "--import-ownertrust"], home,
            (selected["fingerprint"] + ":6:\n").encode())
        run(["gpg", "--batch", "--check-trustdb"], home)
        action = ["-U", "--noconfirm", str(package)]
        if repository is not None:
            action = ["-Syu", "--noconfirm"]
            if not (root / "usr/share" / PACKAGE / "value").exists():
                action.append(expected)
        try:
            subprocess.run(["sudo", "-n", "pacman", "--config", str(config), "--root", str(root),
                            "--dbpath", str(root / "db"), "--cachedir", str(root / "cache"),
                            "--hookdir", str(root / "hooks"), "--gpgdir", str(home),
                            "--logfile", str(root / "pacman.log"), *action], check=True)
        finally:
            subprocess.run(["sudo", "-n", "chown", "-R", f"{os.getuid()}:{os.getgid()}", "--", str(root)], check=True)
    query = root / "query.conf"
    query.write_text("[options]\nArchitecture = " + platform.machine() + "\n")
    owned = subprocess.check_output(["pacman", "--config", str(query), "--root", str(root), "--dbpath", str(root / "db"),
                                     "-Qoq", "--", str(root / "usr/share" / PACKAGE / "value")], text=True).strip()
    if owned != expected:
        raise ValueError("installed fixture file has the wrong package owner")


def rehearse(work: Path, r2_origin: str | None = None):
    os.environ.pop("LINUX_PACKAGE_SIGNING_KEY", None)
    architecture = platform.machine()
    catalog = ephemeral(work / "key")
    wrong = ephemeral(work / "wrong")
    original_catalog = catalog
    rotation = ephemeral(work / "rotation")
    trust = json.loads(rotation.read_text())
    trust["signing_generation"] = trust["generations"][0]["generation"] = 2
    rotation.write_text(json.dumps(trust))
    results = []
    delivery_root = work / "aur-delivery"
    delivery_root.mkdir()
    delivery = FixturePublisher(delivery_root)
    aur_head = None
    active = None
    retained = None
    remote_active = None
    storage_prefix = "release-system-tests/pacman-repositories-" + uuid.uuid4().hex
    if r2_origin is not None:
        print("Pacman repository rehearsal prefix: " + storage_prefix, flush=True)
    for version in ["0.0.1", "0.0.2", "0.0.3"]:
        catalog = rotation if version == "0.0.3" else original_catalog
        operation = "rotate" if version == "0.0.3" else "publish"
        source = work / version
        source.mkdir()
        (source / "PKGBUILD").write_text(f"""pkgname={PACKAGE}
pkgver={version}
pkgrel=1
pkgdesc='Disposable repository fixture'
arch=('{architecture}')
license=('MIT')
options=('!debug' '!strip')
package() {{
  install -d "$pkgdir/usr/share/{PACKAGE}"
  printf '%s\\n' '{version}' > "$pkgdir/usr/share/{PACKAGE}/value"
}}
""")
        subprocess.run(["makepkg", "--nodeps", "--noconfirm", "--cleanbuild", "--force", "--nosign"],
                       cwd=source, check=True)
        packages = list(source.glob("*.pkg.tar.zst"))
        if len(packages) != 1:
            raise ValueError("native fixture construction is ambiguous")
        package = packages[0]
        manifest = {"format_version": 1, "source_commit": "f" * 40, "version": version,
                    "architecture": architecture, "packages": [{"file": package.name,
                    "sha256": digest(package), "native_version": version + "-1"}]}
        destination = work / ("repository-" + version)
        receipt = assemble(manifest, source, destination, "preview", catalog,
                           catalog.parent / "rehearsal-secret.asc", True, PACKAGE)
        admit(receipt, destination, catalog, True)
        rejected(lambda: admit(receipt, destination, wrong, True))
        rejected(lambda: admit(receipt, destination, catalog))
        rejected(lambda: admit({**receipt, "channel": "stable"}, destination, catalog, True))
        selected = destination / package.name
        raw = selected.read_bytes()
        selected.write_bytes(raw + b"changed")
        rejected(lambda: admit(receipt, destination, catalog, True))
        selected.write_bytes(raw)
        absent = selected.with_name(".withheld-" + selected.name)
        selected.rename(absent)
        rejected(lambda: admit(receipt, destination, catalog, True))
        absent.rename(selected)
        admit(receipt, destination, catalog, True)
        origin = work / "origin"
        channel = origin / "channels/pacman/preview" / architecture
        if retained is not None and version == "0.0.2":
            # The package manager must refuse a detached signature for another database.
            alias = channel / (receipt["repository"] + ".db.sig")
            previous_signature = alias.read_bytes()
            replace(alias, (destination / alias.name).read_bytes())
            try:
                install(destination / package.name, work / "mismatched-root", catalog, PACKAGE, channel)
            except subprocess.CalledProcessError:
                pass
            else:
                raise ValueError("pacman accepted a database/signature mismatch")
            replace(alias, previous_signature)
        if operation == "rotate":
            rejected(lambda: publish(receipt, destination, origin, "https://repository.example.invalid", catalog, active, True))
        selected = publish(receipt, destination, origin, "https://repository.example.invalid", catalog, active, True, operation)
        rejected(lambda: publish(receipt, destination, origin, "https://repository.example.invalid", catalog, "0" * 64, True))
        if retained is None:
            retained = receipt, destination
        active = selected["generation"]
        if operation == "rotate":
            try:
                install(destination / package.name, work / "wrong-successor-root", original_catalog, PACKAGE, channel)
            except subprocess.CalledProcessError:
                pass
            else:
                raise ValueError("pacman accepted an unavailable successor signing key")
        install(destination / package.name, work / "native-root", catalog, PACKAGE, channel)
        if (work / "native-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
            raise ValueError("native pacman upgrade did not install the selected payload")
        recipe = work / ("aur-" + version)
        generated = generate([(receipt, destination)], recipe, "https://repository.example.invalid", catalog,
                             True, PACKAGE)
        for row in receipt["objects"]:
            if row["path"].endswith((".pkg.tar.zst", ".pkg.tar.zst.sig")):
                shutil.copyfile(destination / row["path"], recipe / row["path"])
        selected_delivery = aur_delivery_plan(generated, [receipt], recipe,
                                             hashlib.sha256(encoded(manifest)).hexdigest())
        rejected(lambda: delivery.publish(selected_delivery, recipe, "0" * 40))
        pkgbuild = recipe / "PKGBUILD"
        original_recipe = pkgbuild.read_bytes()
        pkgbuild.write_bytes(original_recipe + b"# changed after selection\n")
        rejected(lambda: delivery.publish(selected_delivery, recipe, aur_head))
        pkgbuild.write_bytes(original_recipe)
        delivered = delivery.publish(selected_delivery, recipe, aur_head)
        if delivery.publish(selected_delivery, recipe, aur_head)["commit"] != delivered["commit"]:
            raise ValueError("AUR delivery retry did not retain its exact selected recipe commit")
        aur_head = delivered["commit"]
        selected_key, public = selection(catalog, True)
        with keyring(public, selected_key["fingerprint"]) as home:
            subprocess.run(["makepkg", "--nodeps", "--noconfirm", "--cleanbuild", "--force", "--nosign"],
                           cwd=recipe, env={**os.environ, "GNUPGHOME": str(home)}, check=True)
            altered = recipe / package.name
            original = altered.read_bytes()
            altered.write_bytes(original + b"changed")
            invalid = subprocess.run(["makepkg", "--verifysource", "--force", "--noconfirm"], cwd=recipe,
                                     env={**os.environ, "GNUPGHOME": str(home)})
            altered.write_bytes(original)
            if invalid.returncode == 0:
                raise ValueError("AUR makepkg accepted changed upstream package bytes")
        wrong_key, wrong_public = selection(wrong, True)
        with keyring(wrong_public, wrong_key["fingerprint"]) as home:
            invalid = subprocess.run(["makepkg", "--verifysource", "--force", "--noconfirm"], cwd=recipe,
                                     env={**os.environ, "GNUPGHOME": str(home)})
            if invalid.returncode == 0:
                raise ValueError("AUR makepkg accepted an unavailable upstream signing key")
        rebuilt = list(recipe.glob(PACKAGE + "-preview-bin-*.pkg.tar.zst"))
        if len(rebuilt) != 1 or package_info(rebuilt[0]) != {
                "pkgname": generated["package"], "pkgver": version + "-1", "arch": architecture}:
            raise ValueError("AUR recipe did not preserve the selected release identity")
        binary_signature(rebuilt[0], rebuilt[0].with_name(rebuilt[0].name + ".sig"), catalog,
                         catalog.parent / "rehearsal-secret.asc", True)
        install_yay(recipe, work / "yay-root", catalog, generated["package"], version)
        install(rebuilt[0], work / "aur-root", catalog, generated["package"])
        if (work / "aur-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
            raise ValueError("AUR pacman upgrade did not install the selected payload")
        content = subprocess.check_output(["bsdtar", "-xOf", str(rebuilt[0]),
                                           f"usr/share/{PACKAGE}/value"], timeout=60)
        if content != (version + "\n").encode():
            raise ValueError("AUR recipe changed the authenticated upstream payload")
        if "pkgname = " + generated["package"] not in (recipe / ".SRCINFO").read_text():
            raise ValueError("generated SRCINFO differs from the selected AUR package")
        if version == "0.0.2":
            old_receipt, old_directory = retained
            restored = publish(old_receipt, old_directory, origin, "https://repository.example.invalid", original_catalog,
                               active, True, "withdraw")
            if (work / "native-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
                raise ValueError("repository withdrawal changed an installed fixture")
            active = restored["generation"]
            rejected(lambda: publish(receipt, destination, origin, "https://repository.example.invalid", catalog, active, True))
        if r2_origin is not None:
            if remote_active is None:
                warm_absence(r2_origin + "/" + storage_prefix + "/channels/pacman/preview/" + architecture + "/" + receipt["repository"] + ".db")
            remote_channel = r2_origin + "/" + storage_prefix + "/channels/pacman/preview/" + architecture
            if version == "0.0.2":
                signature = receipt["repository"] + ".db.sig"
                key = storage_prefix + "/channels/pacman/preview/" + architecture + "/" + signature
                put(key, destination / signature, True)
                public_bytes(remote_channel + "/" + signature, destination / signature, True)
                try:
                    install(destination / package.name, work / "r2-mismatched-root", catalog, PACKAGE, remote_channel)
                except subprocess.CalledProcessError:
                    pass
                else:
                    raise ValueError("R2 pacman accepted a database/signature mismatch")
                finally:
                    old_signature = retained[1] / signature
                    put(key, old_signature, True)
                    public_bytes(remote_channel + "/" + signature, old_signature, True)
            staged = publish_r2(receipt, destination, catalog, storage_prefix, r2_origin, remote_active, True, operation, "stage")
            if remote_active is None:
                package_row = receipt["packages"][0]
                key = storage_prefix + "/generations/" + staged["generation"] + "/" + package_row["file"]
                tamper_fixture_object(key, destination / package_row["file"], work / "tampered-object")
                try:
                    publish_r2(receipt, destination, catalog, storage_prefix, r2_origin, None, True)
                except subprocess.CalledProcessError:
                    pass
                else:
                    raise ValueError("R2 admitted a tampered pacman package object")
                finally:
                    delete_fixture_object(key)
            remote = publish_r2(receipt, destination, catalog, storage_prefix, r2_origin, remote_active, True, operation)
            rejected(lambda: publish_r2(receipt, destination, catalog, storage_prefix, r2_origin, "0" * 64, True, operation))
            install(destination / package.name, work / "r2-root", catalog, PACKAGE, remote_channel)
            if (work / "r2-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
                raise ValueError("R2 pacman upgrade did not install the selected payload")
            if version == "0.0.2":
                old_receipt, old_directory = retained
                recovered = publish_r2(old_receipt, old_directory, original_catalog, storage_prefix, r2_origin,
                                       remote["generation"], True, "withdraw")
                remote_active = recovered["generation"]
                rejected(lambda: publish_r2(receipt, destination, catalog, storage_prefix, r2_origin, remote_active, True))
            else:
                remote_active = remote["generation"]
        if version == "0.0.3":
            old_receipt, old_directory = retained
            publish(old_receipt, old_directory, origin, "https://repository.example.invalid", original_catalog,
                    active, True, "recover")
            install(destination / package.name, work / "native-root", original_catalog, PACKAGE, channel)
            if (work / "native-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
                raise ValueError("repository recovery changed the installed version")
            if r2_origin is not None:
                publish_r2(old_receipt, old_directory, original_catalog, storage_prefix, r2_origin,
                           remote_active, True, "recover")
                install(destination / package.name, work / "r2-root", original_catalog, PACKAGE, remote_channel)
                if (work / "r2-root/usr/share" / PACKAGE / "value").read_text() != version + "\n":
                    raise ValueError("R2 repository recovery changed the installed package")
        results.append({"version": version, "objects": len(receipt["objects"])})
    return {"format_version": 1, "rehearsal": True, "application_qualified": False,
            "architecture": platform.machine(), "published": False, "isolated_r2_published": r2_origin is not None,
            "storage_prefix": storage_prefix if r2_origin is not None else None, "generations": results,
            "cases": ["native-makepkg", "signed-database-and-files", "embedded-package-signature",
                      "wrong-key", "production-refuses-fixture", "changed-package", "missing-object",
                      "channel-mismatch", "retained-generations", "pinned-aur-makepkg",
                      "aur-input-signature-and-checksum", "aur-srcinfo", "aur-fixture-Git-delivery", "aur-stale-delivery-refused",
                      "aur-selected-recipe-tamper-refused", "aur-delivery-independent-availability", "aur-payload-identity",
                      "native-repository-Syu-upgrade", "aur-installed-upgrade", "yay-local-recipe-installed-upgrade", "actual-pacman-file-ownership",
                      "pacman-database-signature-mismatch-refused", "stale-publisher-refused",
                      "retained-repository-recovery", "withdrawal-preserves-installed-package",
                      "aur-makepkg-tamper-refused", "aur-makepkg-wrong-key-refused",
                      "explicit-key-rotation", "installed-successor-key-refusal", "withdrawn-reoffer-refused"]
                     + (["r2-pacman-Syu-installed-upgrade", "r2-recovery-preserves-installed-package",
                         "r2-database-signature-mismatch-refused", "r2-stale-publisher-refused"]
                        if r2_origin is not None else [])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--r2", action="store_true")
    args = parser.parse_args()
    try:
        r2_origin = rehearsal_environment() if args.r2 else None
        with tempfile.TemporaryDirectory(prefix="paintedwolf-pacman-rehearsal-") as temporary:
            result = rehearse(Path(temporary), r2_origin)
        if args.r2:
            result["cases"] += ["r2-negative-cache-recovery", "r2-staged-publication-resume",
                                "r2-tampered-object-refused", "r2-missing-object-restored"]
        root = Path(__file__).resolve().parents[2]
        if (root / ".git").exists():
            result["verification_source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            result["verification_source_tree"] = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root, text=True).strip()
            result["verification_source_dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root))
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        args.receipt.write_text(json.dumps(result, indent=2) + "\n")
        print(f"Pacman repository rehearsal passed: {args.receipt}")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Pacman repository rehearsal failed: {exc}\n")


if __name__ == "__main__":
    main()
