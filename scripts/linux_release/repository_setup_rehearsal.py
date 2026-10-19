"""Install and remove opt-in setup packages in private package databases."""

from pathlib import Path
import subprocess

from .repository_installation import restore_owner
from .repository_setup import build, PACKAGE
from .signing import digest, verify


def rehearse(work, manifest, directory, catalog, origin):
    family = manifest["family"]
    output = work / (family + "-setup-package")
    private = catalog.parent / "rehearsal-secret.asc"
    receipt = build(manifest, directory, catalog, output, origin, private, True)
    artifact = output / receipt["package"]["file"]
    verify("detached", output / "SHA256SUMS", output / "SHA256SUMS.asc", catalog, True)
    root = work / (family + "-setup-root")
    root.mkdir()
    data = root / "home/fixture/.local/share/paintedwolf/preserve"
    data.parent.mkdir(parents=True)
    data.write_text("mutable user data\n")
    if family == "deb":
        state = root / "var/lib/dpkg"
        state.mkdir(parents=True)
        (state / "status").touch()
        install = ["dpkg", "--root=" + str(root), "--log=" + str(root / "dpkg.log"), "--install", str(artifact)]
        remove = ["dpkg", "--root=" + str(root), "--log=" + str(root / "dpkg.log"), "--purge", PACKAGE]
        owned = ["dpkg-query", "--admindir=" + str(state), "--listfiles", PACKAGE]
    else:
        subprocess.run(["sudo", "-n", "rpm", "--root", str(root), "--initdb"], check=True)
        install = ["rpm", "--root", str(root), "--nodeps", "--install", str(artifact)]
        remove = ["rpm", "--root", str(root), "--erase", PACKAGE]
        owned = ["sudo", "-n", "rpm", "--root", str(root), "--query", "--list", PACKAGE]
    try:
        subprocess.run(["sudo", "-n", *install], check=True)
        ownership = subprocess.check_output(owned, text=True).splitlines()
        for row in receipt["installed_files"]:
            if "/" + row["path"] not in ownership or digest(root / row["path"]) != row["sha256"]:
                raise ValueError("setup package did not install its exact owned configuration and key")
        if (root / "opt/painted-wolf-code").exists():
            raise ValueError("repository setup unexpectedly installed the application")
        subprocess.run(["sudo", "-n", *remove], check=True)
        if any((root / row["path"]).exists() for row in receipt["installed_files"]):
            raise ValueError("setup removal left its unmodified repository configuration or key")
        if data.read_text() != "mutable user data\n":
            raise ValueError("setup package removal changed mutable user data")
    finally:
        restore_owner(root)
    try:
        build(manifest, directory, catalog, work / (family + "-production-setup"), origin, private)
    except ValueError:
        pass
    else:
        raise ValueError("setup package admitted rehearsal trust in production")
    if (work / (family + "-production-setup")).exists():
        raise ValueError("refused setup allocated an output directory")
