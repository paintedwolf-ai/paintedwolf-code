"""Install disposable signed pacman fixtures into isolated roots and check file ownership."""

from __future__ import annotations

import os
from pathlib import Path
import platform
import subprocess

from .openpgp import keyring, run, selection

PACKAGE = "paintedwolf-repository-fixture"


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
