"""Exercise a pinned local AUR recipe through a real isolated yay client."""

import os
from pathlib import Path
import platform
import shutil
import subprocess

from .openpgp import keyring, run, selection


def install(recipe: Path, root: Path, catalog: Path, package: str, version: str):
    client = shutil.which("yay")
    if client is None:
        raise ValueError("the isolated AUR client rehearsal requires yay")
    root.mkdir(exist_ok=True)
    for directory in ["db", "cache", "hooks", "config", "build"]:
        (root / directory).mkdir(exist_ok=True)
    selected, public = selection(catalog, True)
    fresh = root / ("recipe-" + version)
    shutil.copytree(recipe, fresh, ignore=shutil.ignore_patterns("src", "pkg", package + "-*.pkg.tar.zst*"))
    subprocess.run(["git", "init", "--initial-branch=master", str(fresh)], check=True)
    subprocess.run(["git", "-C", str(fresh), "add", "--", "."], check=True)
    subprocess.run(["git", "-C", str(fresh), "-c", "user.name=Repository fixture",
                    "-c", "user.email=rehearsal@example.invalid", "commit", "-m", "Select authenticated recipe"], check=True)
    origin = root / ("origin-" + version + ".git")
    subprocess.run(["git", "clone", "--bare", str(fresh), str(origin)], check=True)
    subprocess.run(["git", "-C", str(fresh), "remote", "add", "origin", str(origin)], check=True)
    subprocess.run(["git", "-C", str(fresh), "fetch", "origin"], check=True)
    subprocess.run(["git", "-C", str(fresh), "branch", "--set-upstream-to=origin/master"], check=True)
    with keyring(public, selected["fingerprint"]) as home:
        run(["gpg", "--batch", "--import", str(catalog.parent / "rehearsal-secret.asc")], home)
        run(["gpg", "--batch", "--import-ownertrust"], home,
            (selected["fingerprint"] + ":6:\n").encode())
        config = root / "pacman.conf"
        config.write_text(f"""[options]
RootDir = {root}
DBPath = {root}/db
CacheDir = {root}/cache
HookDir = {root}/hooks
LogFile = {root}/pacman.log
GPGDir = {home}
Architecture = {platform.machine()}
SigLevel = Required DatabaseRequired
LocalFileSigLevel = Required
""")
        environment = {**os.environ, "GNUPGHOME": str(home), "XDG_CONFIG_HOME": str(root / "config"),
                       "XDG_CACHE_HOME": str(root / "cache")}
        command = [client, "-Bi", str(fresh), "--noconfirm", "--config", str(config),
                   "--builddir", str(root / "build"), "--mflags", "--sign --force --key " + selected["fingerprint"]]
        try:
            subprocess.run(command, env=environment, check=True)
        finally:
            subprocess.run(["sudo", "-n", "chown", "-R", f"{os.getuid()}:{os.getgid()}", "--", str(root)], check=True)
    owned = subprocess.check_output(["pacman", "--root", str(root), "--dbpath", str(root / "db"),
                                     "-Qoq", "--", str(root / "usr/share/paintedwolf-repository-fixture/value")], text=True).strip()
    if owned != package or (root / "usr/share/paintedwolf-repository-fixture/value").read_text() != version + "\n":
        raise ValueError("yay did not install the selected recipe payload and package identity")
    return subprocess.check_output([client, "--version"], text=True).strip()
