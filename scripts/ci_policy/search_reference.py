"""Provision the pinned native-search reference used by verification."""

from dataclasses import dataclass
import hashlib
from pathlib import Path
import platform
import subprocess
import sys
import tarfile
import tempfile

VERSION = "15.0.0"
MAX_ARCHIVE_BYTES = 32 << 20
MAX_BINARY_BYTES = 32 << 20
PINS = {
    ("Linux", "x86_64"): ("x86_64-unknown-linux-musl", "253ad0fd5fef0d64cba56c70dccdacc1916d4ed70ad057cc525fcdb0c3bbd2a7"),
    ("Linux", "aarch64"): ("aarch64-unknown-linux-gnu", "15f8cc2fab12d88491c54d49f38589922a9d6a7353c29b0a0856727bcdf80754"),
    ("Darwin", "aarch64"): ("aarch64-apple-darwin", "98bb2e61e7277ba0ea72d2ae2592497fd8d2940934a16b122448d302a6637e3b"),
    ("Darwin", "x86_64"): ("x86_64-apple-darwin", "44128c733d127ddbda461e01225a68b5f9997cfe7635242a797f645ca674a71a"),
}


@dataclass(frozen=True)
class Release:
    target: str
    digest: str

    @property
    def member(self):
        return f"ripgrep-{VERSION}-{self.target}/rg"

    @property
    def url(self):
        return f"https://github.com/BurntSushi/ripgrep/releases/download/{VERSION}/ripgrep-{VERSION}-{self.target}.tar.gz"


def host_release():
    machine = {"arm64": "aarch64", "AMD64": "x86_64"}.get(platform.machine(), platform.machine())
    pin = PINS.get((platform.system(), machine))
    if pin is None:
        raise ValueError("No pinned search reference is available for this platform")
    return Release(*pin)


def file_digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def download(url, destination):
    subprocess.run([
        "curl", "--fail", "--location", "--silent", "--show-error", "--retry", "3",
        "--max-time", "30", "--max-filesize", str(MAX_ARCHIVE_BYTES),
        "--output", str(destination), url,
    ], check=True)


def binary_digest(archive, release, output=None):
    with tarfile.open(archive, "r:gz") as bundle:
        member = bundle.getmember(release.member)
        if not member.isfile() or member.size > MAX_BINARY_BYTES:
            raise ValueError("Search reference must contain a bounded regular executable")
        digest = hashlib.sha256()
        with bundle.extractfile(member) as stream:
            while chunk := stream.read(1 << 20):
                digest.update(chunk)
                if output is not None:
                    output.write(chunk)
        return digest.hexdigest()


def verify_existing(destination, release):
    archive, binary = destination / "release.tar.gz", destination / "rg"
    if destination.is_symlink() or any(p.is_symlink() or not p.is_file() for p in (archive, binary)):
        raise ValueError("Existing reference is not verified; choose a fresh destination")
    if archive.stat().st_size > MAX_ARCHIVE_BYTES or file_digest(archive) != release.digest:
        raise ValueError("Existing search reference archive has a different digest")
    if binary_digest(archive, release) != file_digest(binary) or not binary.stat().st_mode & 0o111:
        raise ValueError("Existing search reference executable differs from its verified archive")
    return binary


def ensure(destination, release, fetch=download):
    destination = Path(destination).absolute()
    if destination.exists() or destination.is_symlink():
        return verify_existing(destination, release)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".search-reference-", dir=destination.parent) as temporary:
        candidate = Path(temporary) / "verified"
        candidate.mkdir()
        archive = candidate / "release.tar.gz"
        fetch(release.url, archive)
        if archive.stat().st_size > MAX_ARCHIVE_BYTES or file_digest(archive) != release.digest:
            raise ValueError("Search reference archive digest does not match its pin")
        binary = candidate / "rg"
        with binary.open("xb") as output:
            binary_digest(archive, release, output)
        binary.chmod(0o755)
        candidate.rename(destination)
    return destination / "rg"


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1:
        raise ValueError("Expected one search-reference destination directory")
    binary = ensure(args[0], host_release())
    print(f"Search reference: ripgrep {VERSION} at {binary}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, tarfile.TarError, subprocess.CalledProcessError) as error:
        print(f"Search reference setup failed: {error}", file=sys.stderr)
        sys.exit(1)
