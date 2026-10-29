"""Native package-manager configuration and final authority activation."""

from pathlib import Path

from .repository_objects import immutable, replace


def configuration(receipt: dict, url: str, fingerprint: str) -> str:
    if receipt["family"] == "deb":
        return (f"Types: deb\nURIs: {url}/\nSuites: {receipt['channel']}\nComponents: main\n"
                "Signed-By: /etc/apt/keyrings/painted-wolf-code.asc\n")
    if receipt["family"] != "rpm":
        raise ValueError("native configuration requires deb or rpm")
    return (f"[painted-wolf-code-{receipt['channel']}]\nname=Painted Wolf Code\nmirrorlist={url}/mirrorlist\n"
            "enabled=1\ngpgcheck=1\nrepo_gpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/painted-wolf-code.asc\n"
            f"# Verify the imported key fingerprint: {fingerprint}\n")


def activate(receipt: dict, generation: Path, channel: Path, base_url: str):
    family = receipt["family"]
    if family == "deb":
        authority = f"dists/{receipt['channel']}/InRelease"
        for row in receipt["objects"]:
            name = row["path"]
            if name == authority:
                continue
            target = channel / name
            data = (generation / name).read_bytes()
            if "/by-hash/" in name or name.startswith("pool/"):
                immutable(target, data)
            else:
                replace(target, data)
        replace(channel / authority, (generation / authority).read_bytes())
    elif family == "rpm":
        replace(channel / "mirrorlist",
                (base_url.rstrip("/") + "/generations/" + generation.name + "/\n").encode())
    else:
        raise ValueError("native activation requires deb or rpm")
